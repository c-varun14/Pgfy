package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c-varun14/Pgfy/internal/hoststatus"
	"github.com/c-varun14/Pgfy/internal/postgres"
	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/store"
)

type sink struct {
	mu       sync.Mutex
	messages []Message
	headers  []http.Header
	status   int
	server   *httptest.Server
}

func newSink(t *testing.T) *sink {
	s := &sink{status: 204}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m Message
		json.Unmarshal(body, &m)
		s.mu.Lock()
		s.messages = append(s.messages, m)
		s.headers = append(s.headers, r.Header.Clone())
		status := s.status
		s.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *sink) states(key string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, m := range s.messages {
		if m.Key == key {
			out = append(out, m.State)
		}
	}
	return out
}

type fixture struct {
	engine   *Engine
	store    *store.Store
	now      time.Time
	pgErr    error
	sink     *sink
	host     hoststatus.Paths
	lastFree int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s, e := store.Open(filepath.Join(t.TempDir(), "metadata.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DB.Close() })
	v, _ := security.NewVault(bytes.Repeat([]byte{3}, 32))
	dir := t.TempDir()
	f := &fixture{store: s, now: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC), sink: newSink(t),
		host: hoststatus.Paths{Status: filepath.Join(dir, "host-status.json"), CertSync: filepath.Join(dir, "cert-sync.json"), TLSState: filepath.Join(dir, "state.json"), Certificate: filepath.Join(dir, "server.crt")}}
	f.engine = &Engine{Store: s, Vault: v, InstallationID: "inst", Dashboard: "https://pgfy.example.com", Mode: "tunnel", Version: "test", HostPaths: f.host,
		Postgres: func(context.Context) (string, error) { return "18.6", f.pgErr }, Now: func() time.Time { return f.now }}
	if e := f.engine.SaveWebhook(context.Background(), Webhook{URL: f.sink.server.URL + "/hook/T0KEN", Secret: "shh", PrivateEndpoint: true}); e != nil {
		t.Fatal(e)
	}
	f.lastFree = 80
	return f
}

func (f *fixture) report(freePercent int) {
	body, _ := json.Marshal(map[string]any{"version": 1, "written_at": f.now.Format(time.RFC3339),
		"disks": []map[string]any{{"name": "root", "device": 7, "total_bytes": 100, "free_bytes": freePercent}, {"name": "workspace", "device": 7, "total_bytes": 100, "free_bytes": freePercent}},
		"ntp":   map[string]any{"synchronized": true}})
	os.WriteFile(f.host.Status, body, 0644)
}

func (f *fixture) tick(d time.Duration) {
	f.now = f.now.Add(d)
	f.report(f.lastFree)
	f.engine.Tick(context.Background())
}

func (f *fixture) setFree(p int) { f.lastFree = p }

func TestFiringOnceADayAndResolvedAfterStayingClear(t *testing.T) {
	f := newFixture(t)
	f.setFree(10)
	f.tick(0)
	key := "disk_low:root+workspace"
	if got := f.sink.states(key); len(got) != 1 || got[0] != "firing" {
		t.Fatal("one disk alert for one device", got)
	}
	f.tick(time.Hour)
	if len(f.sink.states(key)) != 1 {
		t.Fatal("repeated within a day")
	}
	f.tick(24 * time.Hour)
	if got := f.sink.states(key); len(got) != 2 {
		t.Fatal("no daily reminder", got)
	}
	f.setFree(50)
	f.tick(time.Minute)
	if len(f.sink.states(key)) != 2 {
		t.Fatal("resolved sent before it stayed clear")
	}
	f.tick(11 * time.Minute)
	if got := f.sink.states(key); len(got) != 3 || got[2] != "resolved" {
		t.Fatal("no resolved message", got)
	}
	// Flapping: firing again within a day of the last firing is held back.
	f.setFree(10)
	f.tick(time.Minute)
	f.setFree(50)
	f.tick(time.Minute)
	f.setFree(10)
	f.tick(time.Minute)
	if got := f.sink.states(key); len(got) != 3 {
		t.Fatal("a flapping condition paged again", got)
	}
}

func TestUnreachablePostgresNeedsFiveMinutesAndUncheckedSourcesResolveNothing(t *testing.T) {
	f := newFixture(t)
	f.setFree(80)
	f.pgErr = errors.New("down")
	f.tick(0)
	f.tick(4 * time.Minute)
	if len(f.sink.states("postgres_unreachable")) != 0 {
		t.Fatal("fired before five minutes")
	}
	f.tick(2 * time.Minute)
	if got := f.sink.states("postgres_unreachable"); len(got) != 1 {
		t.Fatal("did not fire after five minutes", got)
	}
	// A stale host report is not a recovered disk.
	f.setFree(10)
	f.pgErr = nil
	f.tick(time.Minute)
	if len(f.sink.states("disk_low:root+workspace")) != 1 {
		t.Fatal("disk alert missing")
	}
	f.now = f.now.Add(20 * time.Minute) // the report is now stale: nothing is known about disks
	f.engine.Tick(context.Background())
	f.engine.Tick(context.Background())
	for i := 0; i < 3; i++ {
		f.now = f.now.Add(10 * time.Minute)
		f.engine.Tick(context.Background())
	}
	if got := f.sink.states("disk_low:root+workspace"); len(got) != 1 {
		t.Fatal("an unchecked disk was reported resolved", got)
	}
	if got := f.sink.states("host_report_stale"); len(got) != 1 || got[0] != "firing" {
		t.Fatal("a silent host was not reported", got)
	}
}

func TestMaintenanceRecordsAndSendsNothing(t *testing.T) {
	f := newFixture(t)
	f.store.SetMaintenance(context.Background(), true)
	f.setFree(10)
	f.tick(0)
	if len(f.sink.messages) != 0 {
		t.Fatal("sent during maintenance")
	}
	if c, _ := f.store.AlertConditions(context.Background()); len(c) != 0 {
		t.Fatal("recorded during maintenance")
	}
	f.store.SetMaintenance(context.Background(), false)
	f.tick(time.Minute)
	if len(f.sink.states("disk_low:root+workspace")) != 1 {
		t.Fatal("did not resume after maintenance")
	}
}

func TestEventsAreSentOnceSignedAndWithoutSecrets(t *testing.T) {
	f := newFixture(t)
	f.setFree(80)
	ctx := context.Background()
	if e := store.RecordAlertEvent(ctx, f.store.DB, "credential_rotated", "credential_rotated:prj_x", "A database password was changed", "Project prj_x.", f.now); e != nil {
		t.Fatal(e)
	}
	f.sink.status = 500
	f.tick(0)
	f.sink.status = 204
	f.tick(time.Minute)
	f.tick(time.Minute)
	if got := f.sink.states("credential_rotated:prj_x"); len(got) != 2 || got[1] != "event" {
		t.Fatal("an event must be retried after a failure and then sent once", got)
	}
	h := f.sink.headers[len(f.sink.headers)-1]
	ts := h.Get("X-Pgfy-Timestamp")
	if !strings.HasPrefix(h.Get("X-Pgfy-Signature"), "sha256=") || ts == "" {
		t.Fatal("unsigned delivery")
	}
	for _, m := range f.sink.messages {
		raw, _ := json.Marshal(m)
		if strings.Contains(string(raw), "T0KEN") || strings.Contains(string(raw), "shh") {
			t.Fatal("a secret reached the payload")
		}
	}
}

func TestSignatureCoversTimestampAndBody(t *testing.T) {
	var got []byte
	var header http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		header = r.Header.Clone()
	}))
	defer server.Close()
	now := time.Unix(1_800_000_000, 0)
	if e := Deliver(context.Background(), Webhook{URL: server.URL, Secret: "k", PrivateEndpoint: true}, []byte(`{"a":1}`), "v", now); e != nil {
		t.Fatal(e)
	}
	if header.Get("X-Pgfy-Signature") != Sign("k", 1_800_000_000, got) || header.Get("X-Pgfy-Timestamp") != "1800000000" {
		t.Fatal(header)
	}
	if Sign("k", 1_800_000_001, got) == Sign("k", 1_800_000_000, got) {
		t.Fatal("the timestamp is not signed")
	}
}

func TestDestinationsAreCheckedWhereTheyAreDialled(t *testing.T) {
	cases := []struct {
		ip      string
		private bool
		ok      bool
	}{
		{"8.8.8.8", false, true}, {"127.0.0.1", false, false}, {"10.1.2.3", false, false}, {"169.254.169.254", false, false},
		{"10.1.2.3", true, true}, {"127.0.0.1", true, true}, {"100.100.1.1", true, true}, {"fd00::1", true, true},
		{"169.254.169.254", true, false}, {"8.8.8.8", true, false},
		{"64:ff9b::a9fe:a9fe", false, false}, {"64:ff9b::808:808", false, true}, {"2002:0a00:0001::1", false, false}, {"198.18.0.1", false, false}, {"0.1.2.3", false, false},
	}
	for _, c := range cases {
		if allowed(net.ParseIP(c.ip), c.private) != c.ok {
			t.Error(c)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	secret := "https://" + strings.TrimPrefix(server.URL, "http://") + "/services/T0KEN"
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	e := Deliver(context.Background(), Webhook{URL: secret}, []byte(`{}`), "v", time.Now())
	if e == nil || strings.Contains(e.Error(), "T0KEN") || strings.Contains(e.Error(), "/services") {
		t.Fatal("a loopback receiver was contacted in public mode, or the error leaked the URL:", e)
	}
	if (Webhook{URL: "http://hooks.example.com/x"}).Validate() == nil {
		t.Fatal("plain http accepted for a public webhook")
	}
	if got := (Webhook{URL: "https://hooks.slack.com/services/T/B/X"}).Masked(); got != "https://hooks.slack.com/…" {
		t.Fatal(got)
	}
}

func TestConnectionsAreUncheckedWhilePostgresIsDown(t *testing.T) {
	f := newFixture(t)
	f.setFree(80)
	budget := postgres.Budget{Available: 10, ProjectsUsed: 9}
	f.engine.Budget = func(context.Context) (postgres.Budget, error) { return budget, nil }
	f.tick(0)
	if len(f.sink.states("connections_global")) != 1 {
		t.Fatal("80% of available did not fire")
	}
	budget.ProjectsUsed = 1
	f.pgErr = errors.New("down")
	for i := 0; i < 3; i++ {
		f.tick(5 * time.Minute)
	}
	if got := f.sink.states("connections_global"); len(got) != 1 {
		t.Fatal("resolved while it could not be checked", got)
	}
}

func TestNoWebhookSendsNothingAndANewReceiverHearsWhatIsFiring(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.engine.SaveWebhook(ctx, Webhook{})
	f.setFree(10)
	f.tick(0)
	if c, _ := f.store.AlertConditions(ctx); len(c) != 1 || c[0].SentState != "" {
		t.Fatal("without a receiver nothing is marked sent", c)
	}
	f.engine.SaveWebhook(ctx, Webhook{URL: f.sink.server.URL + "/a", PrivateEndpoint: true})
	f.tick(time.Minute)
	if got := f.sink.states("disk_low:root+workspace"); len(got) != 1 {
		t.Fatal("configuring a receiver did not send what is firing", got)
	}
	f.engine.SaveWebhook(ctx, Webhook{URL: f.sink.server.URL + "/b", PrivateEndpoint: true})
	f.tick(time.Minute)
	if got := f.sink.states("disk_low:root+workspace"); len(got) != 2 {
		t.Fatal("a new receiver did not hear the active condition", got)
	}
}

func TestRemovedSourcesResolveTheirConditions(t *testing.T) {
	f := newFixture(t)
	budget := postgres.Budget{Available: 10, ProjectsUsed: 9}
	f.engine.Budget = func(context.Context) (postgres.Budget, error) { return budget, nil }
	f.tick(0)
	f.engine.Budget = nil // management went away: nothing left to watch
	f.tick(time.Minute)
	f.tick(11 * time.Minute)
	if got := f.sink.states("connections_global"); len(got) != 2 || got[1] != "resolved" {
		t.Fatal("a condition whose source no longer exists stayed active", got)
	}
}
