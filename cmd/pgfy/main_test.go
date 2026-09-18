package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMissingMetadataCannotReopenSetup(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "install.json")
	if e := os.WriteFile(cfg, []byte(`{"id":"test","mode":"tunnel","origin":"http://127.0.0.1:8080","generation":"1"}`), 0600); e != nil {
		t.Fatal(e)
	}
	db := filepath.Join(dir, "metadata.db")
	t.Setenv("PGFY_CONFIG", cfg)
	t.Setenv("PGFY_DB", db)
	original := os.Args
	t.Cleanup(func() { os.Args = original })
	os.Args = []string{"pgfy", "setup-token"}
	if e := run(); e == nil {
		t.Fatal("setup token issued without existing metadata")
	}
	if _, e := os.Stat(db); !os.IsNotExist(e) {
		t.Fatal("normal command created missing metadata")
	}
	os.Args = []string{"pgfy", "initialize-store"}
	if e := run(); e != nil {
		t.Fatal(e)
	}
	if e := run(); e == nil {
		t.Fatal("existing metadata reinitialized")
	}
}
