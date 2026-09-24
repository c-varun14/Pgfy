// Package alerts turns what the application and the host know into messages
// for one generic JSON webhook: a condition fires once and is repeated at most
// daily while it lasts, and a resolved message follows once it has stayed clear.
package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/c-varun14/Pgfy/internal/hoststatus"
	"github.com/c-varun14/Pgfy/internal/postgres"
	"github.com/c-varun14/Pgfy/internal/security"
	"github.com/c-varun14/Pgfy/internal/store"
)

const (
	Every             = time.Minute
	PostgresGrace     = 5 * time.Minute
	sendBudget        = 30 * time.Second
	sealContext       = "setting:" + store.AlertWebhookSetting
	sourceBackups     = "backups"
	sourcePostgres    = "postgres"
	sourceHost        = "host"
	sourceHostReport  = "host_report"
	sourceConnections = "connections"
	sourceBucket      = "bucket"
	sourceCertificate = "certificate"
)

// Backups describes the backup target. Configured false means there is no
// storage at all (nothing to alert about); ok false from the callback means it
// exists but could not be read completely yet.
type Backups struct {
	Configured bool
	Target     string
	Interval   time.Duration
}

type Engine struct {
	Store          *store.Store
	Vault          *security.Vault
	InstallationID string
	Dashboard      string
	Mode           string
	Version        string
	HostPaths      hoststatus.Paths
	// Postgres runs the same check as /health/ready.
	Postgres func(context.Context) (string, error)
	// Budget is nil when PostgreSQL management is unavailable.
	Budget func(context.Context) (postgres.Budget, error)
	// Backups reports the storage target; ok is false while it cannot be judged.
	Backups func(context.Context) (Backups, bool)
	Now     func() time.Time

	kick chan struct{}
	mu   sync.Mutex
	// sendMu keeps a receiver change from interleaving with a delivery pass,
	// which would record the old receiver's delivery against the new one.
	sendMu sync.Mutex
	// failingSince is when PostgreSQL started failing; zero while it answers
	// or before the first check after a start or an update.
	failingSince time.Time
	maintenance  bool
	lastOK       int64
	lastError    string
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Kick asks for an evaluation now, e.g. after the webhook settings change.
func (e *Engine) Kick() {
	e.mu.Lock()
	if e.kick == nil {
		e.kick = make(chan struct{}, 1)
	}
	k := e.kick
	e.mu.Unlock()
	select {
	case k <- struct{}{}:
	default:
	}
}

func (e *Engine) Run(ctx context.Context) {
	e.Kick()
	e.mu.Lock()
	k := e.kick
	e.mu.Unlock()
	ticker := time.NewTicker(Every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-k:
		}
		e.Tick(ctx)
	}
}

// Tick evaluates, records and sends. Nothing is recorded or sent while an
// update holds the installation quiet: a rollback would restore the alert
// state and the receiver would hear things twice or not at all.
func (e *Engine) Tick(ctx context.Context) {
	on, err := e.Store.Maintenance(ctx)
	if err != nil {
		return
	}
	e.mu.Lock()
	if on {
		e.maintenance = true
		e.mu.Unlock()
		return
	}
	if e.maintenance {
		// PostgreSQL was restarted on purpose; its failure clock starts afresh.
		e.maintenance, e.failingSince = false, time.Time{}
	}
	e.mu.Unlock()
	now := e.now()
	active, checked := e.Evaluate(ctx, now)
	if err := e.Store.ReconcileAlerts(ctx, active, checked, now); err != nil {
		slog.Warn("alert state not recorded", "reason", err.Error())
		return
	}
	e.send(ctx, now)
}

func condition(kind, key, source, summary, detail string) store.AlertCondition {
	return store.AlertCondition{Kind: kind, Key: key, Source: source, Summary: summary, Detail: detail}
}

// Evaluate returns the conditions true now and which sources were actually
// looked at: a source that could not be checked resolves nothing.
func (e *Engine) Evaluate(ctx context.Context, now time.Time) ([]store.AlertCondition, map[string]bool) {
	var active []store.AlertCondition
	checked := map[string]bool{}
	if e.Postgres != nil {
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, err := e.Postgres(checkCtx)
		cancel()
		e.mu.Lock()
		switch {
		case err == nil:
			e.failingSince = time.Time{}
			checked[sourcePostgres] = true
		case e.failingSince.IsZero():
			e.failingSince = now
		}
		failing := e.failingSince
		e.mu.Unlock()
		if !failing.IsZero() && now.Sub(failing) >= PostgresGrace {
			checked[sourcePostgres] = true
			active = append(active, condition("postgres_unreachable", "postgres_unreachable", sourcePostgres, "PostgreSQL has been unreachable for more than five minutes",
				"Since "+failing.UTC().Format(time.RFC3339)+". Applications cannot connect. Run pgfyctl diagnostics on the server."))
		}
	}
	if e.Budget == nil {
		// Without management there is no budget to watch; stale connection alerts resolve.
		checked[sourceConnections] = true
	} else if checked[sourcePostgres] && len(active) == 0 {
		budgetCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		b, err := e.Budget(budgetCtx)
		cancel()
		if err == nil {
			checked[sourceConnections] = true
			global, roles := b.Warnings()
			if global {
				active = append(active, condition("connections", "connections_global", sourceConnections, "Project connections are at 80% of what PostgreSQL accepts",
					fmt.Sprintf("%d of %d open.", b.ProjectsUsed, b.Available)))
			}
			for _, role := range roles {
				active = append(active, condition("connections", "connections_role:"+role, sourceConnections, "A database is at 80% of its connection limit", "Role "+role+"."))
			}
		}
	}
	host := hoststatus.Read(e.HostPaths, e.Mode, now)
	checked[sourceHostReport] = true
	if host.State != "ok" {
		active = append(active, condition("host_report_stale", "host_report_stale", sourceHostReport, "The host has stopped reporting disk and clock status",
			"Disk and clock alerts cannot fire until it reports again. Check systemctl status pgfy-host-status.timer."))
	} else {
		checked[sourceHost] = true
		active = append(active, diskConditions(host.Disks)...)
		if host.NTPSynchronized != nil && !*host.NTPSynchronized {
			active = append(active, condition("ntp_unsynchronised", "ntp_unsynchronised", sourceHost, "The host clock is not synchronised",
				"Sign-in codes and certificate checks depend on the time. Check systemd-timesyncd."))
		}
	}
	// In tunnel mode there is no database certificate: checked, and clear.
	checked[sourceCertificate] = true
	if e.Mode == "https" {
		c := host.Certificate
		if c.Expiring && c.ExpiresAt != nil {
			summary := "The database and dashboard certificate expires within 14 days"
			if c.Expired {
				summary = "The database and dashboard certificate has expired"
			}
			active = append(active, condition("certificate_expiring", "certificate_expiring", sourceCertificate, summary,
				"Expires "+time.Unix(*c.ExpiresAt, 0).UTC().Format(time.RFC3339)+". Check that ports 80/443 reach Caddy, then run pgfyctl sync-db-cert."))
		}
		if c.LastSync != nil && !c.LastSync.OK {
			active = append(active, condition("certificate_sync_failed", "certificate_sync_failed", sourceCertificate, "Delivering the certificate to PostgreSQL failed", c.LastSync.Message))
		}
	}
	if e.Backups == nil {
		checked[sourceBackups], checked[sourceBucket] = true, true
	} else if b, ok := e.Backups(ctx); ok {
		if !b.Configured {
			// No storage: nothing can be failing, late or incomplete.
			checked[sourceBackups], checked[sourceBucket] = true, true
		} else {
			active = append(active, e.backupConditions(ctx, b, now, checked)...)
		}
	}
	return active, checked
}

// diskConditions keys by the filesystems on a device, so one full disk is one
// alert however many of the measured paths live on it.
func diskConditions(disks []hoststatus.Disk) []store.AlertCondition {
	groups := map[uint64][]hoststatus.Disk{}
	var out []store.AlertCondition
	for _, d := range disks {
		if !d.Low {
			continue
		}
		if d.Device == 0 {
			out = append(out, condition("disk_low", "disk_low:"+d.Name, sourceHost, "Free disk space is under 15%", fmt.Sprintf("%s: %.0f%% free.", d.Name, d.FreePercent)))
			continue
		}
		groups[d.Device] = append(groups[d.Device], d)
	}
	for _, group := range groups {
		names := []string{}
		for _, d := range group {
			names = append(names, d.Name)
		}
		sort.Strings(names)
		out = append(out, condition("disk_low", "disk_low:"+strings.Join(names, "+"), sourceHost, "Free disk space is under 15%",
			fmt.Sprintf("%s: %.0f%% free.", strings.Join(names, ", "), group[0].FreePercent)))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (e *Engine) backupConditions(ctx context.Context, b Backups, now time.Time, checked map[string]bool) []store.AlertCondition {
	var out []store.AlertCondition
	projects, err := e.Store.Projects(ctx)
	if err != nil {
		return nil
	}
	names := map[string]string{}
	for _, p := range projects {
		names[p.ID] = p.Name
	}
	failures, err1 := e.Store.BackupFailures(ctx)
	late, err2 := e.Store.LateProjects(ctx, b.Target, e.InstallationID, b.Interval, now)
	if err1 == nil && err2 == nil {
		checked[sourceBackups] = true
		for id, f := range failures {
			// From the second failed attempt: the first is retried within 15 minutes.
			if f.Failures >= 2 && names[id] != "" {
				out = append(out, condition("backup_failed", "backup_failed:"+id, sourceBackups, "Backups of "+names[id]+" are failing",
					fmt.Sprintf("%d attempts in a row have failed; the next is retried automatically. See the database's Backups tab.", f.Failures)))
			}
		}
		for _, p := range late {
			out = append(out, condition("backup_stale", "backup_stale:"+p.ID, sourceBackups, "The newest recoverable backup of "+p.Name+" is older than its target",
				"Older than 1.5 times the backup target interval. See the database's Backups tab."))
		}
	}
	if prefixes, err := e.Store.PrefixStates(ctx, b.Target); err == nil {
		checked[sourceBucket] = true
		for _, p := range prefixes {
			if p.ManifestOnly > 0 {
				out = append(out, condition("manifest_only", "manifest_only:"+p.DBName, sourceBucket, "The bucket holds backups whose archive is missing",
					fmt.Sprintf("%d manifest(s) for %s without an archive; they are never offered for restore and never deleted automatically.", p.ManifestOnly, p.DBName)))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Message is the JSON body every receiver gets. text makes Slack-style
// incoming webhooks show something readable without an adapter.
type Message struct {
	Version        int    `json:"version"`
	Kind           string `json:"kind"`
	Key            string `json:"key"`
	State          string `json:"state"`
	Summary        string `json:"summary"`
	Detail         string `json:"detail"`
	InstallationID string `json:"installation_id"`
	Dashboard      string `json:"dashboard"`
	At             string `json:"at"`
	Text           string `json:"text"`
}

func (e *Engine) message(o store.Outgoing, now time.Time) []byte {
	prefix := map[string]string{"firing": "Alert", "resolved": "Resolved", "event": "Notice"}[o.State]
	text := fmt.Sprintf("[Pgfy] %s: %s", prefix, o.Summary)
	if o.Detail != "" && o.State != "resolved" {
		text += " — " + o.Detail
	}
	body, _ := json.Marshal(Message{Version: 1, Kind: o.Kind, Key: o.Key, State: o.State, Summary: o.Summary, Detail: o.Detail,
		InstallationID: e.InstallationID, Dashboard: e.Dashboard, At: now.UTC().Format(time.RFC3339), Text: text})
	return body
}

// Webhook returns the configured webhook; ok is false when none is set.
func (e *Engine) Webhook(ctx context.Context) (Webhook, bool, error) {
	var w Webhook
	sealed, err := e.Store.Setting(ctx, store.AlertWebhookSetting)
	if err != nil || sealed == "" {
		return w, false, err
	}
	plain, err := e.Vault.Open(sealContext, sealed)
	if err != nil {
		return w, false, errors.New("the stored webhook cannot be decrypted with this installation key")
	}
	if err = json.Unmarshal(plain, &w); err != nil || w.URL == "" {
		return w, false, err
	}
	return w, true, nil
}

// SaveWebhook seals the settings; a new receiver hears what is firing now.
func (e *Engine) SaveWebhook(ctx context.Context, w Webhook) error {
	e.sendMu.Lock()
	defer e.sendMu.Unlock()
	previous, _, _ := e.Webhook(ctx)
	plain, _ := json.Marshal(w)
	return e.Store.SetAlertWebhook(ctx, e.Vault.Seal(sealContext, plain), previous.URL != w.URL, e.now())
}

func (e *Engine) send(ctx context.Context, now time.Time) {
	e.sendMu.Lock()
	defer e.sendMu.Unlock()
	w, ok, err := e.Webhook(ctx)
	if err != nil {
		e.record(err, now)
		return
	}
	if !ok {
		return
	}
	pending, err := e.Store.AlertsToSend(ctx, now)
	if err != nil || len(pending) == 0 {
		return
	}
	// A dead receiver must not hold up the loop: stop at the first failure and
	// keep the whole pass within a budget; no transaction spans a request.
	deadline := time.Now().Add(sendBudget)
	for _, o := range pending {
		if time.Now().After(deadline) {
			return
		}
		sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := Deliver(sendCtx, w, e.message(o, now), e.Version, now)
		cancel()
		e.record(err, now)
		if err != nil {
			e.Store.AlertAttemptFailed(ctx, o)
			var rejected *Rejected
			if errors.As(err, &rejected) && rejected.Status != 429 && rejected.Status < 500 {
				// This message was refused; the receiver is up, so the rest still go.
				continue
			}
			return
		}
		if err := e.Store.MarkAlertSent(ctx, o, now); err != nil {
			slog.Warn("alert delivery not recorded", "reason", err.Error())
		}
	}
}

func (e *Engine) record(err error, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err == nil {
		e.lastOK, e.lastError = now.Unix(), ""
		return
	}
	e.lastError = err.Error()
	slog.Warn("alert not delivered", "reason", err.Error())
}

// Delivery reports the most recent outcome since the application started.
func (e *Engine) Delivery() (lastOK int64, lastError string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastOK, e.lastError
}

// SendTest delivers a test message now and reports the outcome.
func (e *Engine) SendTest(ctx context.Context) error {
	w, ok, err := e.Webhook(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("no webhook is configured")
	}
	now := e.now()
	err = Deliver(ctx, w, e.message(store.Outgoing{Kind: "test", Key: "test", State: "event", Summary: "Test alert from Pgfy", Detail: "Alerts from this installation will arrive here."}, now), e.Version, now)
	e.record(err, now)
	return err
}
