package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != ":7777" {
		t.Errorf("Addr = %q, want :7777", c.Addr)
	}
	if c.BackupInterval != 30*time.Second {
		t.Errorf("BackupInterval = %v, want 30s", c.BackupInterval)
	}
	if c.SnapshotInterval != time.Hour {
		t.Errorf("SnapshotInterval = %v, want 1h", c.SnapshotInterval)
	}
	if c.VectorEnabled() {
		t.Errorf("VectorEnabled() = true with no QDRANT_URL/TEI_URL set")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("TEMPORALDB_ADDR", ":9999")
	t.Setenv("TEMPORALDB_BACKUP_INTERVAL", "1m")
	t.Setenv("QDRANT_URL", "http://localhost:6333")
	t.Setenv("TEI_URL", "http://localhost:8080")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != ":9999" {
		t.Errorf("Addr = %q, want :9999", c.Addr)
	}
	if c.BackupInterval != time.Minute {
		t.Errorf("BackupInterval = %v, want 1m", c.BackupInterval)
	}
	if !c.VectorEnabled() {
		t.Errorf("VectorEnabled() = false with both QDRANT_URL and TEI_URL set")
	}
}

func TestVectorEnabledRequiresBoth(t *testing.T) {
	t.Setenv("QDRANT_URL", "http://localhost:6333")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.VectorEnabled() {
		t.Errorf("VectorEnabled() = true with only QDRANT_URL set, want false")
	}
}

func TestLoadInvalidDuration(t *testing.T) {
	t.Setenv("TEMPORALDB_RETENTION", "not-a-duration")
	if _, err := Load(); err == nil {
		t.Errorf("Load() with invalid TEMPORALDB_RETENTION: want error, got nil")
	}
}
