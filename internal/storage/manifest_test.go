package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestOversizedManifestIsRejectedOnPublicationAndRead(t *testing.T) {
	if _, e := EncodeManifest(Manifest{ProjectName: strings.Repeat("a", MaxManifestBytes)}); e == nil {
		t.Fatal("an oversized manifest was published")
	}
	// Trailing whitespace is valid JSON, so this must be rejected for its size.
	payload := `{"version":1}` + strings.Repeat(" ", MaxManifestBytes)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.Header().Set("Last-Modified", "Tue, 22 Sep 2026 10:15:30 GMT")
		_, _ = w.Write([]byte(payload))
	}))
	defer provider.Close()
	settings := testSettings()
	settings.Endpoint, settings.PrivateEndpoint, settings.PathStyle = provider.URL, true, true
	client, e := New(settings)
	if e != nil {
		t.Fatal(e)
	}
	_, e = client.Manifest(context.Background(), "pgfy/backups/app_test/20260922T101530Z/manifest.json")
	if e == nil || !strings.Contains(e.Error(), "size limit") {
		t.Fatal("the reader did not enforce the publication limit", e)
	}
}
