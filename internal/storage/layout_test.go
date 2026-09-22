package storage

import (
	"strings"
	"testing"
	"time"
)

func testSettings() Settings {
	return Settings{Endpoint: "https://s3.us-east-1.amazonaws.com", Region: "us-east-1", Bucket: "backups", Prefix: "pgfy",
		AccessKey: "key", SecretKey: "secret", BucketProtection: ProtectionVersioning}
}

func TestSettingsValidation(t *testing.T) {
	valid := testSettings()
	if e := valid.Validate(); e != nil {
		t.Fatal(e)
	}
	// Plain HTTP is only allowed for an endpoint on this machine or network.
	plain := valid
	plain.Endpoint = "http://s3.example.com"
	if e := plain.Validate(); e == nil || !strings.Contains(e.Error(), "https") {
		t.Fatal("public plaintext endpoint accepted", e)
	}
	plain.PrivateEndpoint = true
	if e := plain.Validate(); e == nil {
		t.Fatal("a public address was accepted as a private endpoint")
	}
	loopback := plain
	loopback.Endpoint = "http://127.0.0.1:9000"
	if e := loopback.Validate(); e != nil {
		t.Fatal(e)
	}
	unprotected := valid
	unprotected.BucketProtection = ""
	if e := unprotected.Validate(); e == nil {
		t.Fatal("a bucket with no stated protection was accepted")
	}
	// The store's identity covers everything that changes which objects it holds.
	other := valid
	other.Prefix = "pgfy-2"
	if valid.Target() == other.Target() {
		t.Fatal("two folders shared one identity")
	}
	if valid.Masked().SecretKey == valid.SecretKey {
		t.Fatal("the secret was not masked")
	}
}

func TestLayoutValidation(t *testing.T) {
	for _, name := range []string{"app_0123456789ab", "app_x"} {
		if !ValidDatabaseSegment(name) {
			t.Fatal("rejected a generated database name", name)
		}
	}
	for _, name := range []string{"", "App_1", "../etc", "a/b", strings.Repeat("a", 64)} {
		if ValidDatabaseSegment(name) {
			t.Fatal("accepted", name)
		}
	}
	at, ok := ParseDirectorySegment("20260922T101530Z")
	if !ok || !at.Equal(time.Date(2026, 9, 22, 10, 15, 30, 0, time.UTC)) {
		t.Fatal(at, ok)
	}
	for _, name := range []string{"2026-09-22", "20260922T101530", "latest", "20261322T101530Z"} {
		if _, ok := ParseDirectorySegment(name); ok {
			t.Fatal("accepted", name)
		}
	}
}

func entryFor(t *testing.T, key string) Entry {
	t.Helper()
	c := &Client{settings: testSettings()}
	entry, e := c.EntryFor(key)
	if e != nil {
		t.Fatal(e)
	}
	return entry
}

func TestEntryForRejectsAnythingOutsideTheLayout(t *testing.T) {
	c := &Client{settings: testSettings()}
	good := "pgfy/backups/app_0123456789ab/20260922T101530Z/manifest.json"
	entry, e := c.EntryFor(good)
	if e != nil || entry.ArchiveKey != "pgfy/backups/app_0123456789ab/20260922T101530Z/archive.dump" {
		t.Fatal(entry, e)
	}
	for _, key := range []string{
		"pgfy/backups/app_0123456789ab/20260922T101530Z/dump.tar",
		"pgfy/backups/app_0123456789ab/latest/manifest.json",
		"pgfy/backups/../manifest.json",
		"other/backups/app_0123456789ab/20260922T101530Z/manifest.json",
		"pgfy/backups/app_0123456789ab/20260922T101530Z/nested/manifest.json",
	} {
		if _, e := c.EntryFor(key); e == nil {
			t.Fatal("accepted", key)
		}
	}
}

// A manifest that disagrees with where it sits is damaged: it is never restored
// and never deleted, because nothing about its origin can be trusted.
func TestCheckManifestAgainstItsLocation(t *testing.T) {
	key := "pgfy/backups/app_0123456789ab/20260922T101530Z/manifest.json"
	entry := entryFor(t, key)
	good := Manifest{Version: 1, DBName: "app_0123456789ab", CreatedAt: entry.TakenAt, ArchiveKey: entry.ArchiveKey, ManifestKey: key}
	if e := CheckManifest(good, entry); e != nil {
		t.Fatal(e)
	}
	wrongVersion := good
	wrongVersion.Version = 2
	wrongDatabase := good
	wrongDatabase.DBName = "app_ffffffffffff"
	wrongTime := good
	wrongTime.CreatedAt = entry.TakenAt.Add(time.Hour)
	wrongArchive := good
	wrongArchive.ArchiveKey = "pgfy/backups/app_0123456789ab/20260101T000000Z/archive.dump"
	for _, m := range []Manifest{wrongVersion, wrongDatabase, wrongTime, wrongArchive} {
		if e := CheckManifest(m, entry); e == nil {
			t.Fatal("accepted a manifest that contradicts its location", m)
		}
	}
}

// Backups are grouped and ordered by their timestamp folder, so a bucket that
// holds many databases cannot push one of them out of view.
func TestEntryOrderingIsByTimeNotKey(t *testing.T) {
	entries := []Entry{}
	for _, at := range []string{"20260101T000000Z", "20260922T101530Z", "20260615T120000Z"} {
		entries = append(entries, entryFor(t, "pgfy/backups/app_0123456789ab/"+at+"/manifest.json"))
	}
	sortEntries(entries)
	if entries[0].TakenAt.Year() != 2026 || entries[0].directoryName() != "20260922T101530Z" || entries[2].directoryName() != "20260101T000000Z" {
		t.Fatal(entries)
	}
}

// One listing of a database folder is classified into what can be restored,
// what is incomplete work, and what is not ours to touch.
func TestEntriesFromClassifiesOneListing(t *testing.T) {
	root := "pgfy/backups/app_0123456789ab/"
	entries := EntriesFrom(root, []Object{
		{Key: root + "20260101T000000Z/archive.dump", Size: 10}, {Key: root + "20260101T000000Z/manifest.json", Size: 1},
		{Key: root + "20260102T000000Z/archive.dump", Size: 20},
		{Key: root + "20260103T000000Z/manifest.json", Size: 1},
		{Key: root + "20260104T000000Z/manifest.json", Size: 1}, {Key: root + "20260104T000000Z/notes.txt", Size: 1},
		{Key: root + "latest/manifest.json", Size: 1},
		{Key: root + "20260105T000000Z/nested/manifest.json", Size: 1},
	})
	states := map[string]string{}
	for _, entry := range entries {
		states[entry.directoryName()] = entry.State
	}
	want := map[string]string{"20260101T000000Z": StateComplete, "20260102T000000Z": StateArchiveOnly,
		"20260103T000000Z": StateManifestOnly, "20260104T000000Z": StateDamaged, "latest": StateDamaged}
	for name, state := range want {
		if states[name] != state {
			t.Fatalf("%s: got %q, want %q", name, states[name], state)
		}
	}
	if _, ok := states["20260105T000000Z"]; ok {
		t.Fatal("a folder outside the layout was classified", states)
	}
	if entries[0].directoryName() != "20260104T000000Z" || entries[0].SizeBytes != 0 {
		t.Fatal("entries are not newest first", entries[0])
	}
	if entries[len(entries)-1].directoryName() != "latest" {
		t.Fatal("an unparseable folder should sort last", entries)
	}
}
