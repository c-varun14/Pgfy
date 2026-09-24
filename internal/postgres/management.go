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
func (m *Management) EnsureRole(ctx context.Context, name, password string, connectionLimit int64) error {
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
	if connectionLimit < 1 || connectionLimit > 100 {
		return errors.New("invalid connection limit")
	}
	_, e = m.Pool.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION CONNECTION LIMIT %d", pgx.Identifier{name}.Sanitize(), password, connectionLimit))
	return e
}

// SetPassword replaces a project role's password. Existing sessions keep
// running until they are terminated separately.
func (m *Management) SetPassword(ctx context.Context, role, password string) error {
	if !ValidName(role) || !passwordPattern.MatchString(password) {
		return errors.New("invalid role identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, e := m.Pool.Exec(ctx, fmt.Sprintf("ALTER ROLE %s WITH PASSWORD '%s'", pgx.Identifier{role}.Sanitize(), password))
	return e
}

// LimitSettings renders limits as the role settings PostgreSQL stores, so the
// same strings serve for applying and for detecting drift. Values are
// validated integers, never user text.
func LimitSettings(statementMS, idleMS, tempKB, lockMS int64) map[string]string {
	temp := "-1"
	if tempKB != -1 {
		temp = fmt.Sprintf("%dkB", tempKB)
	}
	return map[string]string{
		"statement_timeout":                   fmt.Sprintf("%dms", statementMS),
		"idle_in_transaction_session_timeout": fmt.Sprintf("%dms", idleMS),
		"temp_file_limit":                     temp,
		"lock_timeout":                        fmt.Sprintf("%dms", lockMS),
	}
}

// ApplyLimits sets the role's guardrails and connection limit together.
// temp_file_limit may only be set on a role by a holder of SET on that
// parameter, which the installer grants to pgfy_mgmt. Only these keys are
// touched: the write freeze lives in the same role settings.
func (m *Management) ApplyLimits(ctx context.Context, role string, settings map[string]string, connectionLimit int64) error {
	if !ValidName(role) || connectionLimit < 1 || connectionLimit > 100 {
		return errors.New("invalid role limits")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, e := m.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	name := pgx.Identifier{role}.Sanitize()
	for _, key := range []string{"statement_timeout", "idle_in_transaction_session_timeout", "temp_file_limit", "lock_timeout"} {
		value, ok := settings[key]
		if !ok || !settingValue.MatchString(value) {
			return errors.New("invalid role limits")
		}
		if _, e = tx.Exec(ctx, fmt.Sprintf("ALTER ROLE %s SET %s = '%s'", name, key, value)); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, fmt.Sprintf("ALTER ROLE %s CONNECTION LIMIT %d", name, connectionLimit)); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func settingsMap(config []string) map[string]string {
	out := map[string]string{}
	for _, item := range config {
		if key, value, ok := strings.Cut(item, "="); ok {
			out[key] = value
		}
	}
	return out
}

// ResetDatabaseLimits removes per-database overrides of the limit keys, which
// would otherwise win over the role-wide guardrails.
func (m *Management) ResetDatabaseLimits(ctx context.Context, role, database string, keys []string) error {
	if !ValidName(role) || !ValidName(database) {
		return errors.New("invalid role identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, key := range keys {
		if _, ok := LimitSettings(0, 0, -1, 0)[key]; !ok {
			return errors.New("invalid setting")
		}
		if _, e := m.Pool.Exec(ctx, fmt.Sprintf("ALTER ROLE %s IN DATABASE %s RESET %s", pgx.Identifier{role}.Sanitize(), pgx.Identifier{database}.Sanitize(), key)); e != nil {
			return e
		}
	}
	return nil
}

var settingValue = regexp.MustCompile(`^(-1|[0-9]{1,10}(ms|kB))$`)

// RoleLimits reports what PostgreSQL currently holds for each project role:
// the role-wide settings (any database) and the connection limit.
type RoleState struct {
	Settings        map[string]string
	ConnectionLimit int64
	// DatabaseSettings are set with ALTER ROLE … IN DATABASE for the role's
	// own database; they take precedence over the role-wide ones.
	DatabaseSettings map[string]string
}

func (m *Management) RoleStates(ctx context.Context) (map[string]RoleState, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Project databases share their role's name, so the role's own database is d.datname = r.rolname.
	rows, e := m.Pool.Query(ctx, `SELECT r.rolname, r.rolconnlimit, COALESCE(s.setconfig, '{}'), COALESCE(ds.setconfig, '{}')
		FROM pg_roles r
		LEFT JOIN pg_db_role_setting s ON s.setrole=r.oid AND s.setdatabase=0
		LEFT JOIN pg_database d ON d.datname=r.rolname
		LEFT JOIN pg_db_role_setting ds ON ds.setrole=r.oid AND ds.setdatabase=d.oid
		WHERE r.rolname ~ '^app_[a-f0-9]{12}$'`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]RoleState{}
	for rows.Next() {
		var name string
		var limit int64
		var config, databaseConfig []string
		if e := rows.Scan(&name, &limit, &config, &databaseConfig); e != nil {
			return nil, e
		}
		state := RoleState{Settings: settingsMap(config), ConnectionLimit: limit, DatabaseSettings: settingsMap(databaseConfig)}
		out[name] = state
	}
	return out, rows.Err()
}

type RoleUse struct {
	Role        string `json:"role"`
	Limit       int64  `json:"limit"`
	Connections int64  `json:"connections"`
}

// Budget compares connection limits and live client sessions with what
// PostgreSQL will accept from ordinary roles.
type Budget struct {
	MaxConnections    int64     `json:"max_connections"`
	SuperuserReserved int64     `json:"superuser_reserved"`
	Reserved          int64     `json:"reserved"`
	Available         int64     `json:"available"`
	ProjectsUsed      int64     `json:"projects_used"`
	ProjectsLimit     int64     `json:"projects_limit"`
	Roles             []RoleUse `json:"roles"`
	System            []RoleUse `json:"system"`
	OtherUsed         int64     `json:"other_used"`
}

func (m *Management) ConnectionBudget(ctx context.Context) (Budget, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var b Budget
	e := m.Pool.QueryRow(ctx, `SELECT current_setting('max_connections')::bigint, current_setting('superuser_reserved_connections')::bigint,
		COALESCE(current_setting('reserved_connections', true), '0')::bigint`).Scan(&b.MaxConnections, &b.SuperuserReserved, &b.Reserved)
	if e != nil {
		return b, e
	}
	b.Available = b.MaxConnections - b.SuperuserReserved - b.Reserved
	rows, e := m.Pool.Query(ctx, `SELECT r.rolname, r.rolconnlimit, count(a.pid)
		FROM pg_roles r LEFT JOIN pg_stat_activity a ON a.usename=r.rolname AND a.backend_type='client backend'
		WHERE r.rolname ~ '^app_[a-f0-9]{12}$' OR r.rolname IN ('pgfy_mgmt','pgfy_health')
		GROUP BY r.rolname, r.rolconnlimit ORDER BY r.rolname`)
	if e != nil {
		return b, e
	}
	defer rows.Close()
	b.Roles, b.System = []RoleUse{}, []RoleUse{}
	for rows.Next() {
		var u RoleUse
		if e := rows.Scan(&u.Role, &u.Limit, &u.Connections); e != nil {
			return b, e
		}
		if ValidName(u.Role) {
			b.Roles = append(b.Roles, u)
			b.ProjectsUsed += u.Connections
			if u.Limit > 0 {
				b.ProjectsLimit += u.Limit
			}
		} else {
			b.System = append(b.System, u)
		}
	}
	if e = rows.Err(); e != nil {
		return b, e
	}
	e = m.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE backend_type='client backend'
		AND usename !~ '^app_[a-f0-9]{12}$' AND usename NOT IN ('pgfy_mgmt','pgfy_health')`).Scan(&b.OtherUsed)
	return b, e
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

// SetWritesFrozen makes new sessions of the project role read-only by default
// (or resets that) and ends its current sessions so the change takes effect at
// once. pgfy_mgmt holds ADMIN OPTION on roles it created and inherits their
// membership, which is what ALTER ROLE and pg_terminate_backend require. The
// freeze is cooperative: a client may still SET transaction_read_only = off.
func (m *Management) SetWritesFrozen(ctx context.Context, database, role string, frozen bool) error {
	if !ValidName(database) || !ValidName(role) {
		return errors.New("invalid project identity")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	statement := "ALTER ROLE %s RESET default_transaction_read_only"
	if frozen {
		statement = "ALTER ROLE %s SET default_transaction_read_only = on"
	}
	if _, e := m.Pool.Exec(ctx, fmt.Sprintf(statement, pgx.Identifier{role}.Sanitize())); e != nil {
		return e
	}
	return m.TerminateSessions(ctx, database, role)
}

// TerminateSessions ends every client session of one project role on its
// database; pooled applications reconnect and pick up the role's settings.
func (m *Management) TerminateSessions(ctx context.Context, database, role string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, e := m.Pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE datname=$1 AND usename=$2 AND backend_type='client backend' AND pid<>pg_backend_pid()`, database, role)
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

// NearLimit reports whether use has reached 80% of a limit; a limit of 0 or
// less (unlimited) never does.
func NearLimit(used, limit int64) bool { return limit > 0 && used*5 >= limit*4 }

// Warnings is the one definition of connection pressure, shared by the
// dashboard and the alerts: project connections at 80% of what ordinary roles
// may open, and any role at 80% of its own limit.
func (b Budget) Warnings() (global bool, roles []string) {
	global = NearLimit(b.ProjectsUsed, b.Available)
	for _, u := range append(append([]RoleUse{}, b.Roles...), b.System...) {
		if NearLimit(u.Connections, u.Limit) {
			roles = append(roles, u.Role)
		}
	}
	return global, roles
}
