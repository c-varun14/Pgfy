package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Management is the bounded pgfy_mgmt connection used only for provisioning,
// measurement, and evidence import. It never touches dashboard requests beyond
// those jobs and cannot become a superuser.
type Management struct {
	Pool     *pgxpool.Pool
	password string
}

var (
	// Generated identifiers only; never user input.
	namePattern      = regexp.MustCompile(`^app_[a-f0-9]{12}$`)
	passwordPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{32,64}$`)
	ErrRoleMismatch  = errors.New("a role with this name exists with different attributes")
	ErrOwnerMismatch = errors.New("a database with this name exists with a different owner")
)

func ValidName(name string) bool { return namePattern.MatchString(name) }

func OpenManagement(ctx context.Context, passwordFile string) (*Management, error) {
	b, e := os.ReadFile(passwordFile)
	if e != nil {
		return nil, e
	}
	password := strings.TrimSpace(string(b))
	c, e := pgxpool.ParseConfig("host=postgres port=5432 user=pgfy_mgmt dbname=pgfy_system sslmode=disable connect_timeout=3")
	if e != nil {
		return nil, e
	}
	c.ConnConfig.Password = password
	c.MaxConns = 4
	c.MinConns = 0
	pool, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		return nil, e
	}
	return &Management{Pool: pool, password: password}, nil
}

// Password is handed to bundled tools through a protected pgpass file only.
func (m *Management) Password() string { return m.password }

// Connect opens one short-lived connection to a specific project database.
// Callers bound every statement with their own deadline.
func (m *Management) Connect(ctx context.Context, database string) (*pgx.Conn, error) {
	if !ValidName(database) {
		return nil, errors.New("invalid database name")
	}
	c, e := pgx.ParseConfig("host=postgres port=5432 user=pgfy_mgmt dbname=" + database + " sslmode=disable connect_timeout=3")
	if e != nil {
		return nil, e
	}
	c.Password = m.password
	return pgx.ConnectConfig(ctx, c)
}

// DatabaseSize reports live bytes; an unavailable measurement is an error,
// never a fabricated zero.
func (m *Management) DatabaseSize(ctx context.Context, database string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var size int64
	e := m.Pool.QueryRow(ctx, "SELECT pg_database_size(datname) FROM pg_database WHERE datname=$1", database).Scan(&size)
	return size, e
}

// EnsureRole creates the restricted project role or verifies that an existing
// one already has the expected attributes, so retries after interruption are safe.
func (m *Management) EnsureRole(ctx context.Context, name, password string) error {
	if !ValidName(name) || !passwordPattern.MatchString(password) {
		return errors.New("invalid role identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var login, super, createdb, createrole bool
	e := m.Pool.QueryRow(ctx, "SELECT rolcanlogin, rolsuper, rolcreatedb, rolcreaterole FROM pg_roles WHERE rolname=$1", name).Scan(&login, &super, &createdb, &createrole)
	if e == nil {
		if !login || super || createdb || createrole {
			return ErrRoleMismatch
		}
		// The role exists from an interrupted attempt; reassert the persisted password.
		_, e = m.Pool.Exec(ctx, fmt.Sprintf("ALTER ROLE %s WITH PASSWORD '%s'", pgx.Identifier{name}.Sanitize(), password))
		return e
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	_, e = m.Pool.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION CONNECTION LIMIT 25", pgx.Identifier{name}.Sanitize(), password))
	return e
}

// EnsureDatabase creates the project database owned by its role and keeps
// other roles out; PUBLIC would otherwise hold CONNECT on every new database.
func (m *Management) EnsureDatabase(ctx context.Context, name, owner string) error {
	if !ValidName(name) || !ValidName(owner) {
		return errors.New("invalid database identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var existingOwner string
	e := m.Pool.QueryRow(ctx, "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname=$1", name).Scan(&existingOwner)
	if e == nil && existingOwner != owner {
		return ErrOwnerMismatch
	}
	if errors.Is(e, pgx.ErrNoRows) {
		if _, e = m.Pool.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s OWNER %s", pgx.Identifier{name}.Sanitize(), pgx.Identifier{owner}.Sanitize())); e != nil {
			return e
		}
	} else if e != nil {
		return e
	}
	_, e = m.Pool.Exec(ctx, fmt.Sprintf("REVOKE ALL ON DATABASE %s FROM PUBLIC", pgx.Identifier{name}.Sanitize()))
	return e
}

type Connection struct {
	ClientAddr      string    `json:"client_addr"`
	TLS             bool      `json:"tls"`
	ApplicationName string    `json:"application_name"`
	Since           time.Time `json:"since"`
}

// Connections lists live sessions of one project role on its database as
// observed right now; it is evidence, not a promise of future connectivity.
func (m *Management) Connections(ctx context.Context, database, role string) ([]Connection, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, e := m.Pool.Query(ctx, `SELECT COALESCE(host(a.client_addr), ''), COALESCE(s.ssl, false), a.application_name, a.backend_start
		FROM pg_stat_activity a LEFT JOIN pg_stat_ssl s ON s.pid=a.pid
		WHERE a.datname=$1 AND a.usename=$2 AND a.backend_type='client backend' ORDER BY a.backend_start`, database, role)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		var c Connection
		if e := rows.Scan(&c.ClientAddr, &c.TLS, &c.ApplicationName, &c.Since); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// HBAFileErrors parses the access policy currently on disk without loading it.
func (m *Management) HBAFileErrors(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, e := m.Pool.Query(ctx, "SELECT COALESCE(file_name, ''), COALESCE(line_number, 0), error FROM pg_hba_file_rules WHERE error IS NOT NULL")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var file, problem string
		var line int
		if e := rows.Scan(&file, &line, &problem); e != nil {
			return nil, e
		}
		out = append(out, fmt.Sprintf("%s line %d: %s", file, line, problem))
	}
	return out, rows.Err()
}

// Reload asks PostgreSQL to load the policy on disk and waits until it reports
// a newer configuration load time, so "applied" means enforcement started.
func (m *Management) Reload(ctx context.Context) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var before time.Time
	if e := m.Pool.QueryRow(ctx, "SELECT pg_conf_load_time()").Scan(&before); e != nil {
		return time.Time{}, e
	}
	// pg_conf_load_time has second granularity; wait so the reload is distinguishable.
	if elapsed := time.Since(before); elapsed < time.Second {
		time.Sleep(time.Second - elapsed + 50*time.Millisecond)
	}
	if _, e := m.Pool.Exec(ctx, "SELECT pg_reload_conf()"); e != nil {
		return time.Time{}, e
	}
	for {
		var after time.Time
		if e := m.Pool.QueryRow(ctx, "SELECT pg_conf_load_time()").Scan(&after); e != nil {
			return time.Time{}, e
		}
		if after.After(before) {
			return after, nil
		}
		select {
		case <-ctx.Done():
			return time.Time{}, errors.New("PostgreSQL did not confirm the configuration reload")
		case <-time.After(200 * time.Millisecond):
		}
	}
}
