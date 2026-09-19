package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
)

type Health struct {
	Pool *pgxpool.Pool
	ID   string
}

func Open(ctx context.Context, passwordFile, id string) (*Health, error) {
	b, e := os.ReadFile(passwordFile)
	if e != nil {
		return nil, e
	}
	c, e := pgxpool.ParseConfig("host=postgres port=5432 user=pgfy_health dbname=pgfy_system sslmode=disable connect_timeout=3")
	if e != nil {
		return nil, e
	}
	c.ConnConfig.Password = strings.TrimSpace(string(b))
	c.MaxConns = 2
	c.MinConns = 0
	pool, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		return nil, e
	}
	return &Health{Pool: pool, ID: id}, nil
}
func (h *Health) Check(ctx context.Context) (string, error) {
	if h == nil {
		return "", errors.New("postgres unavailable")
	}
	var version, id string
	e := h.Pool.QueryRow(ctx, "SELECT current_setting('server_version'), installation_id FROM pgfy_internal.initialization WHERE singleton=TRUE").Scan(&version, &id)
	if e != nil {
		return "", e
	}
	if id != h.ID {
		return "", errors.New("postgres installation identity mismatch")
	}
	if !strings.HasPrefix(version, "18.6 ") && version != "18.6" {
		return "", errors.New("unexpected postgres version")
	}
	return version, nil
}
