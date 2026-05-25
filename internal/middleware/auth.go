package middleware

import (
	"database/sql"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func Auth(jwtSecret string, tokenDB ...*sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization header format"})
			c.Abort()
			return
		}

		tokenString := parts[1]
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			return []byte(jwtSecret), nil
		})

		if err == nil && token.Valid {
			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token claims"})
				c.Abort()
				return
			}
			c.Set("user_id", int64(claims["user_id"].(float64)))
			c.Set("username", claims["username"].(string))
			c.Next()
			return
		}

		if len(tokenDB) > 0 && tokenDB[0] != nil {
			var userID int64
			var username string
			err := tokenDB[0].QueryRow(`SELECT u.id, u.username FROM drive_api_tokens t JOIN users u ON u.id = t.user_id WHERE t.token = ?`, tokenString).Scan(&userID, &username)
			if err == nil {
				_, _ = tokenDB[0].Exec(`UPDATE drive_api_tokens SET last_used_at = CURRENT_TIMESTAMP WHERE user_id = ?`, userID)
				c.Set("user_id", userID)
				c.Set("username", username)
				c.Next()
				return
			}
		}

		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		c.Abort()
	}
}
