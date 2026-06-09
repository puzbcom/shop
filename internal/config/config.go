// Package config loads application configuration from a .env file and the
// process environment.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Config holds all runtime configuration for the mall application.
type Config struct {
	AppEnv       string
	AppPort      string
	AppKey       string
	DBHost       string
	DBPort       string
	DBUser       string
	DBPassword   string
	DBName       string
	UploadsDir   string
	GeminiAPIKey string
	OllamaURL    string
	OllamaModel  string
}

// Load reads .env (if present) then the environment and returns a Config.
func Load() Config {
	loadDotEnv(".env")
	port := env("APP_PORT", env("PORT", "8080"))
	return Config{
		AppEnv:     env("APP_ENV", "development"),
		AppPort:    port,
		AppKey:     env("APP_KEY", ""),
		DBHost:     env("DB_HOST", "127.0.0.1"),
		DBPort:     env("DB_PORT", "3306"),
		DBUser:     env("DB_USER", "root"),
		DBPassword: env("DB_PASSWORD", ""),
		DBName:     env("DB_NAME", "mall"),
		UploadsDir:   env("UPLOADS_DIR", "uploads"),
		GeminiAPIKey: env("GEMINI_API_KEY", ""),
		OllamaURL:    env("OLLAMA_URL", "http://222.186.58.41:11434/v1/chat/completions"),
		OllamaModel:  env("OLLAMA_MODEL", "deepseek-r1:8b"),
	}
}

// IsDev reports whether the application runs in the development environment.
func (c Config) IsDev() bool { return c.AppEnv == "development" }

// DSN is the GORM/database DSN for the application database.
func (c Config) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=Local",
		c.DBUser, c.DBPassword, c.DBHost, c.DBPort, c.DBName)
}

// ServerDSN connects to the MySQL server without selecting a database; used to
// create the database during migration.
func (c Config) ServerDSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/?charset=utf8mb4&parseTime=true&loc=Local",
		c.DBUser, c.DBPassword, c.DBHost, c.DBPort)
}

// MigrateDSN connects to the application database for running migrations.
func (c Config) MigrateDSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=Local",
		c.DBUser, c.DBPassword, c.DBHost, c.DBPort, c.DBName)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}
