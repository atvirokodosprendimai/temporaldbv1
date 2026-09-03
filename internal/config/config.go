// Package config loads TemporalDB's runtime configuration from the process
// environment, optionally seeded from a .env file (ADR-001 D12).
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
)

// Config holds every runtime setting TemporalDB reads at startup.
type Config struct {
	Addr           string
	DataDir        string
	BackupDir      string
	BackupInterval time.Duration
	Retention      time.Duration // 0 disables purge-by-age
	QdrantURL      string
	QdrantAPIKey   string
	TEIURL         string
	TEIRerankURL   string
}

// VectorEnabled reports whether vector search is configured. Per ADR-001 D6,
// vector search is optional and additive: it is enabled only when both
// Qdrant and TEI are set, and the rest of the system is fully functional
// when it is not.
func (c Config) VectorEnabled() bool {
	return c.QdrantURL != "" && c.TEIURL != ""
}

// RerankEnabled reports whether a TEI reranker endpoint is configured.
// Meaningless when VectorEnabled is false.
func (c Config) RerankEnabled() bool {
	return c.TEIRerankURL != ""
}

// Load reads configuration from the process environment. If a .env file is
// present in the working directory it is loaded first; godotenv never
// overrides a variable already set in the real environment, and the file's
// absence is not an error.
func Load() (Config, error) {
	_ = godotenv.Load()

	c := Config{
		Addr:         getEnv("TEMPORALDB_ADDR", ":7777"),
		DataDir:      getEnv("TEMPORALDB_DATA_DIR", "./data"),
		BackupDir:    getEnv("TEMPORALDB_BACKUP_DIR", "./data/backup"),
		QdrantURL:    getEnv("QDRANT_URL", ""),
		QdrantAPIKey: getEnv("QDRANT_API_KEY", ""),
		TEIURL:       getEnv("TEI_URL", ""),
		TEIRerankURL: getEnv("TEI_RERANK_URL", ""),
	}

	var err error
	if c.BackupInterval, err = getDuration("TEMPORALDB_BACKUP_INTERVAL", 30*time.Second); err != nil {
		return Config{}, err
	}
	if c.Retention, err = getDuration("TEMPORALDB_RETENTION", 0); err != nil {
		return Config{}, err
	}

	return c, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s=%q: %w", key, v, err)
	}
	return d, nil
}
