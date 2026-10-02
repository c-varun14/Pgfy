package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDailyCopyKeepsOnePerDayAndSevenDays(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	source := filepath.Join(base, "pgfy.db")
	s, e := Open(source)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	// Written through the open WAL connection, so the copy must see it.
	if e := s.SetSetting(ctx, "marker", "copied", time.Now()); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(base, "daily")
	day := time.Date(2026, 10, 2, 23, 30, 0, 0, time.UTC)
	name, e := DailyCopy(ctx, s, source, dir, day)
	if e != nil || filepath.Base(name) != "pgfy-20261002.db" {
		t.Fatal(name, e)
	}
	if e := CheckSnapshot(ctx, name); e != nil {
		t.Fatal(e)
	}
	copied, e := Open(name)
	if e != nil {
		t.Fatal(e)
	}
	if v, _ := copied.Setting(ctx, "marker"); v != "copied" {
		t.Fatal("the copy misses a committed row", v)
	}
	copied.DB.Close()
	if again, e := DailyCopy(ctx, s, source, dir, day.Add(20*time.Minute)); e != nil || again != "" {
		t.Fatal("a second copy on the same UTC day", again, e)
	}
	// Nothing is copied while an update holds the installation quiet.
	s.SetMaintenance(ctx, true)
	if quiet, e := DailyCopy(ctx, s, source, dir, day.Add(48*time.Hour)); e != nil || quiet != "" {
		t.Fatal("copied during an update", quiet, e)
	}
	s.SetMaintenance(ctx, false)
	os.WriteFile(filepath.Join(dir, "pgfy-20260901.db.tmp"), []byte("partial"), 0600)
	for i := 1; i <= 8; i++ {
		if _, e := DailyCopy(ctx, s, source, dir, day.Add(time.Duration(i)*24*time.Hour)); e != nil {
			t.Fatal(e)
		}
	}
	copies, _ := filepath.Glob(filepath.Join(dir, "pgfy-*"))
	if len(copies) != DailyCopies || filepath.Base(copies[0]) != "pgfy-20261004.db" {
		t.Fatal(copies)
	}
}
