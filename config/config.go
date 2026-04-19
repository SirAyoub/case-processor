package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	// MinIO
	MinioEndpoint  string
	MinioAccessKey string
	MinioSecretKey string
	MinioBucket    string
	MinioUseSSL    bool

	// Ollama
	OllamaEndpoint       string
	OllamaModel          string
	OllamaMaxPagesChunk  int
	OllamaTimeoutPerPage time.Duration

	// Database
	DBHost     string
	DBPort     int
	DBName     string
	DBUser     string
	DBPassword string

	// Processing
	TempDir           string
	MaxRetries        int
	ConcurrentWorkers int
	PDFDPI            int
}

func Load() (*Config, error) {
	// Load .env file (ignore error if not exists - use env vars directly)
	_ = godotenv.Load()

	timeoutSecs := getEnvInt("OLLAMA_TIMEOUT_PER_PAGE_SECONDS", 180)

	cfg := &Config{
		MinioEndpoint:        getEnv("MINIO_ENDPOINT", "localhost:9000"),
		MinioAccessKey:       mustEnv("MINIO_ACCESS_KEY"),
		MinioSecretKey:       mustEnv("MINIO_SECRET_KEY"),
		MinioBucket:          getEnv("MINIO_BUCKET", "Cases"),
		MinioUseSSL:          getEnvBool("MINIO_USE_SSL", false),
		OllamaEndpoint:       getEnv("OLLAMA_ENDPOINT", "http://localhost:11434"),
		OllamaModel:          getEnv("OLLAMA_MODEL", "gemma4"),
		OllamaMaxPagesChunk:  getEnvInt("OLLAMA_MAX_PAGES_PER_CHUNK", 30),
		OllamaTimeoutPerPage: time.Duration(timeoutSecs) * time.Second,
		DBHost:               getEnv("DB_HOST", "localhost"),
		DBPort:               getEnvInt("DB_PORT", 1433),
		DBName:               mustEnv("DB_NAME"),
		DBUser:               mustEnv("DB_USER"),
		DBPassword:           mustEnv("DB_PASSWORD"),
		TempDir:              getEnv("TEMP_DIR", "/tmp/case-processor"),
		MaxRetries:           getEnvInt("MAX_RETRIES", 2),
		ConcurrentWorkers:    getEnvInt("CONCURRENT_WORKERS", 2),
		PDFDPI:               getEnvInt("PDF_DPI", 200),
	}

	return cfg, nil
}

func (c *Config) DBConnectionString() string {
	return fmt.Sprintf(
		"sqlserver://%s:%s@%s:%d?database=%s&encrypt=disable",
		c.DBUser, c.DBPassword, c.DBHost, c.DBPort, c.DBName,
	)
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required env var %s is not set", key))
	}
	return v
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return defaultVal
}
