package handlers

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const defaultDriveQuotaGB = 20

type DriveHandler struct {
	db      *sql.DB
	dataDir string
}

type driveItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Folder    bool   `json:"folder"`
	Parent    string `json:"parent"`
	Mime      string `json:"mime"`
	Size      int64  `json:"size"`
	Starred   bool   `json:"starred"`
	Trashed   bool   `json:"trashed"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	DeletedAt string `json:"deleted_at,omitempty"`
}

func NewDriveHandler(db *sql.DB, dataDir string) *DriveHandler {
	return &DriveHandler{db: db, dataDir: dataDir}
}

func (h *DriveHandler) State() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		quotaGB := h.ensureQuota(userID)
		items, err := h.listItems(userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取网盘数据失败"})
			return
		}
		used, err := h.usedBytes(userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "统计网盘容量失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items, "quota_gb": quotaGB, "used_bytes": used})
	}
}

func (h *DriveHandler) CreateFolder() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		var req struct {
			Name   string `json:"name"`
			Parent string `json:"parent"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
			return
		}
		name := cleanDriveName(req.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "文件夹名称不能为空"})
			return
		}
		if err := h.validateParent(userID, req.Parent); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		id, err := newDriveID("fld")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "生成文件夹 ID 失败"})
			return
		}
		now := time.Now().Format("2006-01-02")
		_, err = h.db.Exec(`INSERT INTO drive_items (id, user_id, name, is_folder, parent_id, updated_at) VALUES (?, ?, ?, 1, ?, CURRENT_TIMESTAMP)`, id, userID, name, req.Parent)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "创建文件夹失败"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"item": driveItem{ID: id, Name: name, Folder: true, Parent: req.Parent, CreatedAt: now, UpdatedAt: now}})
	}
}

func (h *DriveHandler) UploadFiles() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		parent := c.PostForm("parent")
		if err := h.validateParent(userID, parent); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		form, err := c.MultipartForm()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "未找到上传文件"})
			return
		}
		files := form.File["files"]
		if len(files) == 0 {
			files = form.File["file"]
		}
		if len(files) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "未选择文件"})
			return
		}
		var incoming int64
		for _, fh := range files {
			if fh.Size < 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "文件大小无效"})
				return
			}
			incoming += fh.Size
		}
		quotaBytes := int64(h.ensureQuota(userID)) * 1024 * 1024 * 1024
		used, err := h.usedBytes(userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "统计网盘容量失败"})
			return
		}
		if used+incoming > quotaBytes {
			c.JSON(http.StatusBadRequest, gin.H{"error": "网盘空间不足，请先扩容或清理文件"})
			return
		}

		storageDir := h.userStorageDir(userID)
		if err := os.MkdirAll(storageDir, 0755); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "创建存储目录失败"})
			return
		}

		created := make([]driveItem, 0, len(files))
		for _, fh := range files {
			id, err := newDriveID("f")
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "生成文件 ID 失败"})
				return
			}
			name := cleanDriveName(fh.Filename)
			if name == "" {
				name = id
			}
			storagePath := filepath.Join(storageDir, id)
			if err := saveUploadedFile(fh, storagePath); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "保存文件失败: " + err.Error()})
				return
			}
			mimeType := fh.Header.Get("Content-Type")
			if mimeType == "" {
				mimeType = mime.TypeByExtension(filepath.Ext(name))
			}
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
			_, err = h.db.Exec(`INSERT INTO drive_items (id, user_id, name, is_folder, parent_id, mime, size, storage_path, updated_at) VALUES (?, ?, ?, 0, ?, ?, ?, ?, CURRENT_TIMESTAMP)`, id, userID, name, parent, mimeType, fh.Size, storagePath)
			if err != nil {
				os.Remove(storagePath)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "写入文件记录失败"})
				return
			}
			created = append(created, driveItem{ID: id, Name: name, Parent: parent, Mime: mimeType, Size: fh.Size})
		}
		c.JSON(http.StatusCreated, gin.H{"items": created})
	}
}

func (h *DriveHandler) UpdateItem() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		id := c.Param("id")
		var req struct {
			Name    *string `json:"name"`
			Parent  *string `json:"parent"`
			Starred *bool   `json:"starred"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
			return
		}
		if !h.itemExists(userID, id, false) {
			c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
			return
		}
		if req.Name != nil {
			name := cleanDriveName(*req.Name)
			if name == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "名称不能为空"})
				return
			}
			if _, err := h.db.Exec(`UPDATE drive_items SET name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ? AND trashed = 0`, name, id, userID); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "重命名失败"})
				return
			}
		}
		if req.Parent != nil {
			parent := *req.Parent
			if err := h.validateParent(userID, parent); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			if parent == id {
				c.JSON(http.StatusBadRequest, gin.H{"error": "不能移动到自身"})
				return
			}
			desc, err := h.collectDescendants(userID, id)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "检查目录关系失败"})
				return
			}
			if containsString(desc, parent) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "不能移动到自己的子文件夹"})
				return
			}
			if _, err := h.db.Exec(`UPDATE drive_items SET parent_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ? AND trashed = 0`, parent, id, userID); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "移动失败"})
				return
			}
		}
		if req.Starred != nil {
			starred := 0
			if *req.Starred {
				starred = 1
			}
			if _, err := h.db.Exec(`UPDATE drive_items SET starred = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND user_id = ? AND trashed = 0`, starred, id, userID); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "更新星标失败"})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"message": "已更新"})
	}
}

func (h *DriveHandler) TrashItem() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		id := c.Param("id")
		ids, err := h.collectDescendants(userID, id)
		if err != nil || len(ids) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
			return
		}
		if _, err := h.db.Exec(fmt.Sprintf(`UPDATE drive_items SET trashed = 1, deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE user_id = ? AND id IN (%s)`, placeholders(len(ids))), appendArgs(userID, ids)...); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "移入回收站失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "已移入回收站"})
	}
}

func (h *DriveHandler) RestoreItem() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		id := c.Param("id")
		ids, err := h.collectDescendants(userID, id)
		if err != nil || len(ids) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
			return
		}
		if _, err := h.db.Exec(fmt.Sprintf(`UPDATE drive_items SET trashed = 0, deleted_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE user_id = ? AND id IN (%s)`, placeholders(len(ids))), appendArgs(userID, ids)...); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "还原失败"})
			return
		}
		var parent string
		_ = h.db.QueryRow(`SELECT COALESCE(parent_id, '') FROM drive_items WHERE user_id = ? AND id = ?`, userID, id).Scan(&parent)
		if parent != "" && !h.itemExists(userID, parent, false) {
			_, _ = h.db.Exec(`UPDATE drive_items SET parent_id = '', updated_at = CURRENT_TIMESTAMP WHERE user_id = ? AND id = ?`, userID, id)
		}
		c.JSON(http.StatusOK, gin.H{"message": "已还原"})
	}
}

func (h *DriveHandler) PurgeItem() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		id := c.Param("id")
		ids, err := h.collectDescendants(userID, id)
		if err != nil || len(ids) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "项目不存在"})
			return
		}
		paths, err := h.storagePaths(userID, ids)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取文件路径失败"})
			return
		}
		if _, err := h.db.Exec(fmt.Sprintf(`DELETE FROM drive_items WHERE user_id = ? AND id IN (%s)`, placeholders(len(ids))), appendArgs(userID, ids)...); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "删除失败"})
			return
		}
		for _, p := range paths {
			_ = os.Remove(p)
		}
		c.JSON(http.StatusOK, gin.H{"message": "已彻底删除"})
	}
}

func (h *DriveHandler) ClearTrash() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		rows, err := h.db.Query(`SELECT storage_path FROM drive_items WHERE user_id = ? AND trashed = 1 AND is_folder = 0 AND storage_path <> ''`, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "读取回收站失败"})
			return
		}
		var paths []string
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil && p != "" {
				paths = append(paths, p)
			}
		}
		rows.Close()
		if _, err := h.db.Exec(`DELETE FROM drive_items WHERE user_id = ? AND trashed = 1`, userID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "清空回收站失败"})
			return
		}
		for _, p := range paths {
			_ = os.Remove(p)
		}
		c.JSON(http.StatusOK, gin.H{"message": "回收站已清空"})
	}
}

func (h *DriveHandler) UpdateQuota() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("user_id")
		var req struct {
			QuotaGB int `json:"quota_gb"`
		}
		if err := c.ShouldBindJSON(&req); err != nil || req.QuotaGB <= 0 || req.QuotaGB > 102400 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "容量必须在 1 到 102400 GB 之间"})
			return
		}
		used, err := h.usedBytes(userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "统计网盘容量失败"})
			return
		}
		if int64(req.QuotaGB)*1024*1024*1024 < used {
			c.JSON(http.StatusBadRequest, gin.H{"error": "总容量不能小于当前已用空间"})
			return
		}
		_, err = h.db.Exec(`INSERT INTO drive_settings (user_id, quota_gb, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP) ON CONFLICT(user_id) DO UPDATE SET quota_gb = excluded.quota_gb, updated_at = CURRENT_TIMESTAMP`, userID, req.QuotaGB)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "保存容量设置失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"quota_gb": req.QuotaGB})
	}
}

func (h *DriveHandler) DownloadFile() gin.HandlerFunc {
	return func(c *gin.Context) {
		h.serveFile(c, true)
	}
}

func (h *DriveHandler) PreviewFile() gin.HandlerFunc {
	return func(c *gin.Context) {
		h.serveFile(c, false)
	}
}

func (h *DriveHandler) serveFile(c *gin.Context, attachment bool) {
	userID := c.GetInt64("user_id")
	id := c.Param("id")
	var name, mimeType, storagePath string
	var isFolder int
	err := h.db.QueryRow(`SELECT name, mime, storage_path, is_folder FROM drive_items WHERE user_id = ? AND id = ?`, userID, id).Scan(&name, &mimeType, &storagePath, &isFolder)
	if err == sql.ErrNoRows || isFolder == 1 {
		c.JSON(http.StatusNotFound, gin.H{"error": "文件不存在"})
		return
	}
	if err != nil || storagePath == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取文件失败"})
		return
	}
	if _, err := os.Stat(storagePath); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "文件数据不存在"})
		return
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	c.Header("Content-Type", mimeType)
	disposition := "inline"
	if attachment {
		disposition = "attachment"
	}
	c.Header("Content-Disposition", fmt.Sprintf("%s; filename*=UTF-8''%s", disposition, url.PathEscape(name)))
	http.ServeFile(c.Writer, c.Request, storagePath)
}

func (h *DriveHandler) ensureQuota(userID int64) int {
	_, _ = h.db.Exec(`INSERT OR IGNORE INTO drive_settings (user_id, quota_gb) VALUES (?, ?)`, userID, defaultDriveQuotaGB)
	var quota int
	if err := h.db.QueryRow(`SELECT quota_gb FROM drive_settings WHERE user_id = ?`, userID).Scan(&quota); err != nil || quota <= 0 {
		return defaultDriveQuotaGB
	}
	return quota
}

func (h *DriveHandler) listItems(userID int64) ([]driveItem, error) {
	rows, err := h.db.Query(`SELECT id, name, is_folder, COALESCE(parent_id, ''), COALESCE(mime, ''), size, starred, trashed, COALESCE(created_at, ''), COALESCE(updated_at, ''), COALESCE(deleted_at, '') FROM drive_items WHERE user_id = ? ORDER BY is_folder DESC, name COLLATE NOCASE ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []driveItem{}
	for rows.Next() {
		var it driveItem
		var folder, starred, trashed int
		if err := rows.Scan(&it.ID, &it.Name, &folder, &it.Parent, &it.Mime, &it.Size, &starred, &trashed, &it.CreatedAt, &it.UpdatedAt, &it.DeletedAt); err != nil {
			return nil, err
		}
		it.Folder = folder == 1
		it.Starred = starred == 1
		it.Trashed = trashed == 1
		items = append(items, it)
	}
	return items, rows.Err()
}

func (h *DriveHandler) usedBytes(userID int64) (int64, error) {
	var used sql.NullInt64
	err := h.db.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM drive_items WHERE user_id = ? AND is_folder = 0`, userID).Scan(&used)
	if err != nil {
		return 0, err
	}
	return used.Int64, nil
}

func (h *DriveHandler) validateParent(userID int64, parent string) error {
	if parent == "" {
		return nil
	}
	var isFolder int
	err := h.db.QueryRow(`SELECT is_folder FROM drive_items WHERE user_id = ? AND id = ? AND trashed = 0`, userID, parent).Scan(&isFolder)
	if err == sql.ErrNoRows {
		return fmt.Errorf("目标文件夹不存在")
	}
	if err != nil {
		return fmt.Errorf("检查目标文件夹失败")
	}
	if isFolder != 1 {
		return fmt.Errorf("目标不是文件夹")
	}
	return nil
}

func (h *DriveHandler) itemExists(userID int64, id string, allowTrashed bool) bool {
	query := `SELECT 1 FROM drive_items WHERE user_id = ? AND id = ?`
	args := []any{userID, id}
	if !allowTrashed {
		query += ` AND trashed = 0`
	}
	var one int
	return h.db.QueryRow(query, args...).Scan(&one) == nil
}

func (h *DriveHandler) collectDescendants(userID int64, id string) ([]string, error) {
	if id == "" {
		return nil, nil
	}
	if !h.itemExists(userID, id, true) {
		return nil, nil
	}
	ids := []string{id}
	queue := []string{id}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		rows, err := h.db.Query(`SELECT id FROM drive_items WHERE user_id = ? AND parent_id = ?`, userID, parent)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var child string
			if rows.Scan(&child) == nil {
				ids = append(ids, child)
				queue = append(queue, child)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func (h *DriveHandler) storagePaths(userID int64, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := h.db.Query(fmt.Sprintf(`SELECT storage_path FROM drive_items WHERE user_id = ? AND is_folder = 0 AND storage_path <> '' AND id IN (%s)`, placeholders(len(ids))), appendArgs(userID, ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil && p != "" {
			paths = append(paths, p)
		}
	}
	return paths, rows.Err()
}

func (h *DriveHandler) userStorageDir(userID int64) string {
	return filepath.Join(h.dataDir, "gotee_drive", fmt.Sprintf("user_%d", userID))
}

func saveUploadedFile(fh *multipart.FileHeader, target string) error {
	src, err := fh.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, src)
	return err
}

func cleanDriveName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.Trim(name, ".")
	if name == "." || name == string(os.PathSeparator) {
		return ""
	}
	if len([]rune(name)) > 180 {
		runes := []rune(name)
		name = string(runes[:180])
	}
	return name
}

func newDriveID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b), nil
}

func placeholders(n int) string {
	if n <= 0 {
		return "''"
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func appendArgs(userID int64, ids []string) []any {
	args := make([]any, 0, len(ids)+1)
	args = append(args, userID)
	for _, id := range ids {
		args = append(args, id)
	}
	return args
}

func containsString(values []string, target string) bool {
	if target == "" {
		return false
	}
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
