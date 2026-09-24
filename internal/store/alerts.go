package store

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// Alert timing: one firing message per condition per day, and a resolved
// message only once a condition has stayed clear, so a flapping condition does
// not page on every pass.
const (
	AlertRepeat      = 24 * time.Hour
	AlertClearAfter  = 10 * time.Minute
	AlertEventMaxAge = 24 * time.Hour
)

type AlertCondition struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	Summary     string `json:"summary"`
	Detail      string `json:"detail"`
	Active      bool   `json:"active"`
	FirstSeenAt int64  `json:"first_seen_at"`
	LastFiredAt int64  `json:"last_fired_at"`
	SentState   string `json:"sent_state"`
}

// RecordAlertEvent queues a one-shot event inside the caller's transaction, so
// the event exists exactly when the change it reports does.
func RecordAlertEvent(ctx context.Context, x execer, kind, key, summary, detail string, now time.Time) error {
	_, e := x.ExecContext(ctx, "INSERT INTO alert_events(kind,key,summary,detail,created_at) VALUES (?,?,?,?,?)", kind, key, summary, detail, now.Unix())
	return e
}

// ReconcileAlerts records the conditions true now. A known condition that is
// missing is marked clear only if its source was actually checked this pass:
// "not looked" must never read as "resolved".
func (s *Store) ReconcileAlerts(ctx context.Context, active []AlertCondition, checked map[string]bool, now time.Time) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	seen := map[string]bool{}
	for _, c := range active {
		seen[c.Key] = true
		if _, e = tx.ExecContext(ctx, `INSERT INTO alert_conditions(key,kind,source,summary,detail,active,first_seen_at,last_seen_at) VALUES (?,?,?,?,?,1,?,?)
			ON CONFLICT(key) DO UPDATE SET kind=excluded.kind, source=excluded.source, summary=excluded.summary, detail=excluded.detail,
			first_seen_at=CASE WHEN active=1 OR clear_since IS NOT NULL AND ?-clear_since < ? THEN first_seen_at ELSE excluded.first_seen_at END,
			active=1, last_seen_at=excluded.last_seen_at, clear_since=NULL`,
			c.Key, c.Kind, c.Source, c.Summary, c.Detail, now.Unix(), now.Unix(), now.Unix(), int64(AlertClearAfter.Seconds())); e != nil {
			return e
		}
	}
	rows, e := tx.QueryContext(ctx, "SELECT key, source FROM alert_conditions WHERE active=1")
	if e != nil {
		return e
	}
	var clear []string
	for rows.Next() {
		var key, source string
		if e = rows.Scan(&key, &source); e != nil {
			rows.Close()
			return e
		}
		if !seen[key] && checked[source] {
			clear = append(clear, key)
		}
	}
	rows.Close()
	// A condition awaiting its resolved message must be seen clear by an actual
	// check for the whole wait; an unchecked pass restarts the wait.
	for source := range allSources(tx, ctx) {
		if !checked[source] {
			if _, e = tx.ExecContext(ctx, "UPDATE alert_conditions SET clear_since=? WHERE active=0 AND sent_state='firing' AND source=?", now.Unix(), source); e != nil {
				return e
			}
		}
	}
	for _, key := range clear {
		if _, e = tx.ExecContext(ctx, "UPDATE alert_conditions SET active=0, clear_since=? WHERE key=?", now.Unix(), key); e != nil {
			return e
		}
	}
	// Forget conditions long resolved; the receiver has heard their end.
	if _, e = tx.ExecContext(ctx, "DELETE FROM alert_conditions WHERE active=0 AND sent_state<>'firing' AND clear_since < ?", now.Add(-30*24*time.Hour).Unix()); e != nil {
		return e
	}
	var dropped int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM alert_events WHERE sent_at IS NULL AND created_at < ?", now.Add(-AlertEventMaxAge).Unix()).Scan(&dropped); e != nil {
		return e
	}
	if dropped > 0 {
		slog.Warn("alert events were never delivered and are dropped after 24 hours", "count", dropped)
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM alert_events WHERE (sent_at IS NOT NULL AND sent_at < ?) OR (sent_at IS NULL AND created_at < ?)",
		now.Add(-30*24*time.Hour).Unix(), now.Add(-AlertEventMaxAge).Unix()); e != nil {
		return e
	}
	return tx.Commit()
}

// Outgoing is one message to deliver: a condition firing or resolved, or an event.
type Outgoing struct {
	Key     string
	Kind    string
	State   string // firing, resolved or event
	Summary string
	Detail  string
	EventID int64
	At      int64
}

func (s *Store) AlertsToSend(ctx context.Context, now time.Time) ([]Outgoing, error) {
	out := []Outgoing{}
	rows, e := s.DB.QueryContext(ctx, `SELECT key, kind, summary, detail, CASE WHEN active=1 THEN 'firing' ELSE 'resolved' END, last_seen_at FROM alert_conditions
		WHERE (active=1 AND (last_fired_at IS NULL OR last_fired_at <= ?))
		   OR (active=0 AND sent_state='firing' AND clear_since <= ?)
		ORDER BY first_seen_at`, now.Add(-AlertRepeat).Unix(), now.Add(-AlertClearAfter).Unix())
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var o Outgoing
		if e = rows.Scan(&o.Key, &o.Kind, &o.Summary, &o.Detail, &o.State, &o.At); e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, o)
	}
	rows.Close()
	rows, e = s.DB.QueryContext(ctx, "SELECT id, key, kind, summary, detail, created_at FROM alert_events WHERE sent_at IS NULL AND created_at >= ? ORDER BY id", now.Add(-AlertEventMaxAge).Unix())
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		o := Outgoing{State: "event"}
		if e = rows.Scan(&o.EventID, &o.Key, &o.Kind, &o.Summary, &o.Detail, &o.At); e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// MarkAlertSent records what the receiver has now heard.
func (s *Store) MarkAlertSent(ctx context.Context, o Outgoing, now time.Time) error {
	var e error
	switch o.State {
	case "event":
		_, e = s.DB.ExecContext(ctx, "UPDATE alert_events SET sent_at=?, attempts=attempts+1 WHERE id=?", now.Unix(), o.EventID)
	case "firing":
		_, e = s.DB.ExecContext(ctx, "UPDATE alert_conditions SET sent_state='firing', last_fired_at=? WHERE key=?", now.Unix(), o.Key)
	default:
		_, e = s.DB.ExecContext(ctx, "UPDATE alert_conditions SET sent_state='resolved' WHERE key=? AND active=0", o.Key)
	}
	return e
}

func (s *Store) AlertAttemptFailed(ctx context.Context, o Outgoing) {
	if o.State == "event" {
		_, _ = s.DB.ExecContext(ctx, "UPDATE alert_events SET attempts=attempts+1 WHERE id=?", o.EventID)
	}
}

// ResetAlertDelivery makes a newly configured receiver hear what is firing now.
func (s *Store) ResetAlertDelivery(ctx context.Context) error {
	_, e := s.DB.ExecContext(ctx, "UPDATE alert_conditions SET sent_state='', last_fired_at=NULL WHERE active=1")
	return e
}

func (s *Store) AlertConditions(ctx context.Context) ([]AlertCondition, error) {
	rows, e := s.DB.QueryContext(ctx, "SELECT key,kind,source,summary,detail,active,first_seen_at,COALESCE(last_fired_at,0),sent_state FROM alert_conditions ORDER BY active DESC, first_seen_at DESC LIMIT 200")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []AlertCondition{}
	for rows.Next() {
		var c AlertCondition
		if e = rows.Scan(&c.Key, &c.Kind, &c.Source, &c.Summary, &c.Detail, &c.Active, &c.FirstSeenAt, &c.LastFiredAt, &c.SentState); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func allSources(tx *sql.Tx, ctx context.Context) map[string]bool {
	out := map[string]bool{}
	rows, e := tx.QueryContext(ctx, "SELECT DISTINCT source FROM alert_conditions WHERE active=0 AND sent_state='firing'")
	if e != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var source string
		if rows.Scan(&source) == nil {
			out[source] = true
		}
	}
	return out
}

// SetAlertWebhook stores the sealed webhook settings and, when the receiver
// changes, makes it hear what is firing now — in one transaction.
func (s *Store) SetAlertWebhook(ctx context.Context, sealed string, receiverChanged bool, now time.Time) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "INSERT INTO settings(key,sealed_value,updated_at) VALUES (?,?,?) ON CONFLICT(key) DO UPDATE SET sealed_value=excluded.sealed_value, updated_at=excluded.updated_at", AlertWebhookSetting, sealed, now.Unix()); e != nil {
		return e
	}
	if receiverChanged {
		if _, e = tx.ExecContext(ctx, "UPDATE alert_conditions SET sent_state='', last_fired_at=NULL WHERE active=1"); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE alert_conditions SET sent_state='' WHERE active=0"); e != nil {
			return e
		}
	}
	return tx.Commit()
}

const AlertWebhookSetting = "alerts_webhook"
