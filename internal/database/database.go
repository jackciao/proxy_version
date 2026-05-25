package database

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

func Initialize(dbPath string) (*sql.DB, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	// Create tables
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		email TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS nodes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		protocol TEXT NOT NULL,
		domain TEXT,
		port INTEGER NOT NULL,
		status TEXT DEFAULT 'stopped',
		config TEXT,
		warp_enabled INTEGER DEFAULT 0,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id)
	);

	CREATE TABLE IF NOT EXISTS certificates (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		domain TEXT UNIQUE NOT NULL,
		cert_path TEXT,
		key_path TEXT,
		provider TEXT,
		expires_at DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT
	);

	CREATE TABLE IF NOT EXISTS warp_config (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account_type TEXT DEFAULT 'free',
		device_id TEXT,
		access_token TEXT,
		private_key TEXT,
		public_key TEXT,
		ipv4_address TEXT,
		ipv6_address TEXT,
		endpoint TEXT,
		license_key TEXT,
		team_name TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS drive_items (
		id TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL,
		name TEXT NOT NULL,
		is_folder INTEGER NOT NULL DEFAULT 0,
		parent_id TEXT DEFAULT '',
		mime TEXT DEFAULT '',
		size INTEGER NOT NULL DEFAULT 0,
		storage_path TEXT DEFAULT '',
		starred INTEGER NOT NULL DEFAULT 0,
		trashed INTEGER NOT NULL DEFAULT 0,
		deleted_at DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id)
	);

	CREATE INDEX IF NOT EXISTS idx_drive_items_user_parent ON drive_items(user_id, parent_id, trashed);
	CREATE INDEX IF NOT EXISTS idx_drive_items_user_trashed ON drive_items(user_id, trashed);

	CREATE TABLE IF NOT EXISTS drive_settings (
		user_id INTEGER PRIMARY KEY,
		quota_gb INTEGER NOT NULL DEFAULT 20,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (user_id) REFERENCES users(id)
	);
	`

	_, err = db.Exec(schema)
	if err != nil {
		return nil, err
	}

	// Run migrations for existing databases
	migrations := []string{
		// Add warp_enabled column to nodes table if it doesn't exist
		"ALTER TABLE nodes ADD COLUMN warp_enabled INTEGER DEFAULT 0",
	}

	for _, m := range migrations {
		// Ignore errors for migrations (column may already exist)
		db.Exec(m)
	}

	return db, nil
}
