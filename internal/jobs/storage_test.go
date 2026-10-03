package jobs

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/storage"
)

func TestStorageClientKeepsTargetAfterSettingsChange(t *testing.T) {
	w, _ := testWorker(t, time.Now())
	var e error
	w.Vault, e = security.NewVault(bytes.Repeat([]byte{1}, 32))
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	original := testSettings()
	if e := w.SaveStorageSettings(ctx, original); e != nil {
		t.Fatal(e)
	}
	client, e := w.StorageClient(ctx)
	if e != nil {
		t.Fatal(e)
	}
	changed := original
	changed.Bucket, changed.Prefix = "another-bucket", "another-prefix"
	if e := w.SaveStorageSettings(ctx, changed); e != nil {
		t.Fatal(e)
	}
	current, e := w.StorageSettings(ctx)
	if e != nil || current.Target() != changed.Target() {
		t.Fatal(current, e)
	}
	if client.Target() != original.Target() || client.Target() == current.Target() {
		t.Fatal("the upload client adopted new provenance", client.Target())
	}
	if key := client.BackupKey("app_test", w.now()); !strings.HasPrefix(key, original.Prefix+"/") {
		t.Fatal("the upload client changed its destination", key)
	}
}

// Exercise the real encoder and S3 reader with the maximum captured table count.
// HTML characters expand to six bytes in JSON, which is worse than quotes.
func TestMaximumTableManifestCanBeRead(t *testing.T) {
	for _, identifier := range []string{`"`, `<`} {
		t.Run(identifier, func(t *testing.T) {
			key := "pgfy/backups/app_test/20260922T101530Z/manifest.json"
			at, _ := time.Parse(storage.TimeLayout, "20260922T101530Z")
			manifest := storage.Manifest{Version: 1, DBName: "app_test", CreatedAt: at, ArchiveKey: strings.TrimSuffix(key, storage.ManifestFile) + storage.ArchiveFile, ManifestKey: key}
			for i := 0; i < MaxTables; i++ {
				manifest.Tables = append(manifest.Tables, storage.TableCount{Schema: strings.Repeat(identifier, 63), Name: strings.Repeat(identifier, 58) + fmt.Sprintf("%05d", i), Rows: math.MaxInt64})
			}
			encoded, e := storage.EncodeManifest(manifest)
			if e != nil {
				t.Fatal(e)
			}
			if len(encoded) <= 1<<20 {
				t.Fatal("the regression fixture did not exceed the former reader limit", len(encoded))
			}
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Length", fmt.Sprint(len(encoded)))
				w.Header().Set("Last-Modified", at.Format(http.TimeFormat))
				_, _ = w.Write(encoded)
			}))
			defer provider.Close()
			settings := testSettings()
			settings.Endpoint, settings.PrivateEndpoint, settings.PathStyle = provider.URL, true, true
			client, e := storage.New(settings)
			if e != nil {
				t.Fatal(e)
			}
			decoded, e := client.Manifest(context.Background(), key)
			if e != nil {
				t.Fatal("a successfully published manifest cannot be read", e)
			}
			if len(decoded.Tables) != MaxTables || decoded.Tables[MaxTables-1] != manifest.Tables[MaxTables-1] {
				t.Fatal("the table list was truncated")
			}
		})
	}
}
