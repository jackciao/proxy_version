package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
)

type Config struct {
	Port         string
	JWTSecret    string
	DatabasePath string
	DataDir      string
	Environment  string
}

func Load() *Config {
	return &Config{
		Port:         getEnv("PORT", "8080"),
		JWTSecret:    getEnv("JWT_SECRET", generateSecret()),
		DatabasePath: getEnv("DATABASE_PATH", "./data/proxy_version.db"),
		DataDir:      getEnv("DATA_DIR", "./data"),
		Environment:  getEnv("ENVIRONMENT", "development"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func generateSecret() string {
	bytes := make([]byte, 32)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}
