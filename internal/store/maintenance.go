package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ErrMaintenance means a host-side update holds the installation quiet: no job
// may start and nothing may be written that a rollback would have to undo.
var ErrMaintenance = errors.New("an update is in progress")

// maintenanceGuard is appended to statements that must not take effect while
// an update holds the installation quiet. It is evaluated inside the statement,
// so a flag set between a check and the write cannot be missed.
const maintenanceGuard = "NOT EXISTS(SELECT 1 FROM metadata WHERE key='maintenance' AND value='1')"

func (s *Store) SetMaintenance(ctx context.Context, on bool) error {
	if !on {
		_, e := s.DB.ExecContext(ctx, "DELETE FROM metadata WHERE key='maintenance'")
		return e
	}
	_, e := s.DB.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES ('maintenance','1') ON CONFLICT(key) DO UPDATE SET value=excluded.value")
	return e
}

func (s *Store) Maintenance(ctx context.Context) (bool, error) {
	return maintenance(ctx, s.DB)
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func maintenance(ctx context.Context, q queryer) (bool, error) {
	var on bool
	e := q.QueryRowContext(ctx, "SELECT NOT "+maintenanceGuard).Scan(&on)
	return on, e
}

// RunningJobs counts heavy jobs the worker is executing right now.
func (s *Store) RunningJobs(ctx context.Context) (int, error) {
	var n int
	e := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM jobs WHERE state='running'").Scan(&n)
	return n, e
}

// Snapshot copies the metadata database with VACUUM INTO. It deliberately
// opens the file without migrating: an updater snapshots with the release that
// owns the data, and a restore must work whatever schema the file carries.
func Snapshot(ctx context.Context, source, target string) error {
	if _, e := os.Stat(source); e != nil {
		return errors.New("management storage is missing")
	}
	if e := os.Remove(target); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	db, e := rawOpen(source)
	if e != nil {
		return e
	}
	defer db.Close()
	if _, e = db.ExecContext(ctx, "VACUUM INTO ?", target); e != nil {
		return fmt.Errorf("snapshot failed: %w", e)
	}
	if e = os.Chmod(target, 0600); e != nil {
		return e
	}
	if e = syncFile(target); e != nil {
		return e
	}
	return CheckSnapshot(ctx, target)
}

// CheckSnapshot proves a snapshot exists and is a sound SQLite database.
func CheckSnapshot(ctx context.Context, path string) error {
	if _, e := os.Stat(path); e != nil {
		return errors.New("snapshot is missing")
	}
	db, e := rawOpen(path)
	if e != nil {
		return e
	}
	defer db.Close()
	var result string
	if e = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); e != nil {
		return fmt.Errorf("snapshot is unreadable: %w", e)
	}
	if result != "ok" {
		return errors.New("snapshot failed its integrity check")
	}
	return nil
}

// Restore puts a checked snapshot back in place of the database. The WAL and
// shared-memory files are removed first: frames written by a newer binary
// would otherwise be replayed onto the restored file. The snapshot is kept so
// a failed rollback can be retried.
func Restore(ctx context.Context, snapshot, database string) error {
	if e := CheckSnapshot(ctx, snapshot); e != nil {
		return e
	}
	content, e := os.ReadFile(snapshot)
	if e != nil {
		return e
	}
	staged := database + ".restore"
	if e = os.WriteFile(staged, content, 0600); e != nil {
		return e
	}
	if e = syncFile(staged); e != nil {
		return e
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if e = os.Remove(database + suffix); e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if e = os.Rename(staged, database); e != nil {
		return e
	}
	return syncFile(filepath.Dir(database))
}

func rawOpen(path string) (*sql.DB, error) {
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	if _, e = db.Exec("PRAGMA busy_timeout=5000"); e != nil {
		db.Close()
		return nil, e
	}
	return db, nil
}

func syncFile(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}

// DailyCopies is how many daily copies of management storage are kept.
const DailyCopies = 7

// DailyCopy writes one consistent copy of management storage per UTC day into
// dir and keeps the newest DailyCopies. The copies sit on the same disk: they
// cover a damaged or mistakenly replaced database, not a lost server, which is
// recovered from the bucket. Nothing is copied while an update holds the
// installation quiet, so a rollback never leaves a copy of the newer schema.
func DailyCopy(ctx context.Context, s *Store, source, dir string, now time.Time) (string, error) {
	if on, e := s.Maintenance(ctx); e != nil || on {
		return "", e
	}
	name := filepath.Join(dir, "pgfy-"+now.UTC().Format("20060102")+".db")
	if _, e := os.Stat(name); e == nil {
		return "", nil
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return "", e
	}
	staged := name + ".tmp"
	if e := Snapshot(ctx, source, staged); e != nil {
		_ = os.Remove(staged)
		return "", e
	}
	if e := os.Rename(staged, name); e != nil {
		return "", e
	}
	if e := syncFile(dir); e != nil {
		return "", e
	}
	// Pruned only after this copy is in place, so it is never the one removed.
	copies, _ := filepath.Glob(filepath.Join(dir, "pgfy-*.db"))
	sort.Strings(copies)
	for i := 0; i < len(copies)-DailyCopies; i++ {
		_ = os.Remove(copies[i])
	}
	stray, _ := filepath.Glob(filepath.Join(dir, "pgfy-*.db.tmp"))
	for _, path := range stray {
		_ = os.Remove(path)
	}
	return name, nil
}
