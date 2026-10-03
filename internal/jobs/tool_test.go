package jobs

import (
	"context"
	"strings"
	"testing"
	"time"
)

func runScript(t *testing.T, script string, timeout time.Duration) toolRun {
	t.Helper()
	w := &Worker{Now: time.Now}
	return w.run(context.Background(), "/dev/null", timeout, "/bin/sh", "-c", script)
}

// A restore that ran and complained is a result: the count is honest, its
// source is named, and the excerpt is only for display.
func TestRestoreErrorsAreCountedOverTheWholeStream(t *testing.T) {
	run := runScript(t, `for i in $(seq 1 200); do echo "pg_restore: error: could not execute query $i" >&2; done; exit 1`, 30*time.Second)
	if run.Err != nil {
		t.Fatal(run.Err)
	}
	if run.Exit != 1 || run.Errors != 200 {
		t.Fatal("errors were counted from the excerpt, not the stream", run.Exit, run.Errors)
	}
	if len(run.Excerpt) > 4096+200 || !strings.Contains(run.Excerpt, "pg_restore: error:") {
		t.Fatal("the excerpt is unbounded or empty", len(run.Excerpt))
	}
	if run.Counted != "counted from the output" {
		t.Fatal(run.Counted)
	}
	// The tool's own summary is preferred when it prints one.
	run = runScript(t, `echo "pg_restore: error: one" >&2; echo "pg_restore: warning: errors ignored on restore: 7" >&2; exit 1`, 30*time.Second)
	if run.Errors != 7 || run.Counted != "reported by the tool" {
		t.Fatal(run.Errors, run.Counted)
	}
}

// A diagnostic line longer than any buffer is skipped past, not stopped at:
// the tool must not block on a full pipe, and later lines still count.
func TestOverlongDiagnosticLineIsDrained(t *testing.T) {
	run := runScript(t, `head -c 3000000 /dev/zero | tr '\0' x >&2; echo >&2; for i in $(seq 1 2000); do echo "pg_restore: error: query $i failed with a long enough message" >&2; done; echo "pg_restore: warning: errors ignored on restore: 2001" >&2; exit 1`, 20*time.Second)
	if run.Err != nil {
		t.Fatal("the tool stalled on an overlong line", run.Err)
	}
	if run.Exit != 1 || run.Errors != 2001 || run.Counted != "reported by the tool" {
		t.Fatal("lines after the overlong one were lost", run.Exit, run.Errors, run.Counted)
	}
	if len(run.Excerpt) > 4096+200 {
		t.Fatal("the excerpt is unbounded", len(run.Excerpt))
	}
}

// Being killed, never starting, or running out of time are failures: nothing
// about the database can be claimed afterwards.
func TestToolFailuresAreNotRestoreResults(t *testing.T) {
	killed := runScript(t, `kill -TERM $$; sleep 5`, 30*time.Second)
	if killed.Err == nil || !strings.Contains(killed.Err.Error(), "stopped before it finished") {
		t.Fatal("a killed process was treated as an ordinary exit", killed)
	}
	w := &Worker{Now: time.Now}
	missing := w.run(context.Background(), "/dev/null", time.Second, "/nonexistent/pg_restore")
	if missing.Err == nil || !strings.Contains(missing.Err.Error(), "could not be started") {
		t.Fatal("a tool that never started was treated as a result", missing)
	}
	timedOut := runScript(t, `sleep 5`, 100*time.Millisecond)
	if timedOut.Err == nil {
		t.Fatal("a timeout was treated as a result", timedOut)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := w.run(cancelled, "/dev/null", time.Second, "/bin/sh", "-c", "sleep 5")
	if stopped.Err == nil {
		t.Fatal("a cancelled restore was treated as a result", stopped)
	}
	// A clean run is a clean result.
	ok := runScript(t, `echo done`, 30*time.Second)
	if ok.Err != nil || ok.Exit != 0 || ok.Errors != 0 {
		t.Fatal(ok)
	}
}
