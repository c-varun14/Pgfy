package storage

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

// Backups live at <prefix>/backups/<database>/<timestamp>/{archive.dump,manifest.json}.
// Nothing outside that shape is ever offered for restore or deleted: an object
// of unknown origin in the bucket may belong to another installation.
const (
	ArchiveFile  = "archive.dump"
	ManifestFile = "manifest.json"
	TimeLayout   = "20060102T150405Z"
)

// Entry states. "complete" is the only recoverable one; "damaged" covers
// anything that breaks the layout rules and is always left untouched.
const (
	StateComplete     = "complete"
	StateManifestOnly = "manifest_only"
	StateArchiveOnly  = "archive_only"
	StateDamaged      = "damaged"
)

var (
	databaseName  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	directoryName = regexp.MustCompile(`^\d{8}T\d{6}Z$`)
)

// ValidDatabaseSegment and ValidDirectorySegment are the shared rules applied on
// every path: listing, reconciliation, retention, and the restore lookup.
func ValidDatabaseSegment(name string) bool { return databaseName.MatchString(name) }

func ParseDirectorySegment(name string) (time.Time, bool) {
	if !directoryName.MatchString(name) {
		return time.Time{}, false
	}
	at, e := time.Parse(TimeLayout, name)
	return at.UTC(), e == nil
}

type Entry struct {
	Directory   string    `json:"directory"`
	ManifestKey string    `json:"manifest_key"`
	ArchiveKey  string    `json:"archive_key"`
	TakenAt     time.Time `json:"taken_at"`
	State       string    `json:"state"`
	SizeBytes   int64     `json:"size_bytes"`
}

// ListDatabases returns the database prefixes under the configured folder.
// Segments that do not match the generated database-name shape are ignored.
func (c *Client) ListDatabases(ctx context.Context) ([]string, error) {
	root := c.key("backups") + "/"
	var out []string
	for object := range c.mc.ListObjects(ctx, c.settings.Bucket, minio.ListObjectsOptions{Prefix: root}) {
		if object.Err != nil {
			return nil, errors.New(describe(object.Err))
		}
		name := strings.TrimSuffix(strings.TrimPrefix(object.Key, root), "/")
		if name != "" && ValidDatabaseSegment(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ListBackups classifies one database prefix, newest first, through full
// pagination. Manifests are not read here: the caller reads only the ones it
// has not already recorded, so reconciliation does not re-download every
// manifest on every pass.
func (c *Client) ListBackups(ctx context.Context, dbName string) ([]Entry, error) {
	if !ValidDatabaseSegment(dbName) {
		return nil, errors.New("not a database folder written by this application")
	}
	root := c.key("backups", dbName) + "/"
	var objects []Object
	for object := range c.mc.ListObjects(ctx, c.settings.Bucket, minio.ListObjectsOptions{Prefix: root, Recursive: true}) {
		if object.Err != nil {
			return nil, errors.New(describe(object.Err))
		}
		objects = append(objects, Object{Key: object.Key, Size: object.Size})
	}
	return EntriesFrom(root, objects), nil
}

type Object struct {
	Key  string
	Size int64
}

// EntriesFrom applies the layout rules to one complete listing. A folder with
// an unexpected name or an unexpected file is damaged: it is never offered and
// never deleted, because it may not be ours.
func EntriesFrom(root string, objects []Object) []Entry {
	type found struct {
		archive, manifest, unexpected bool
		size                          int64
	}
	directories := map[string]*found{}
	for _, object := range objects {
		parts := strings.Split(strings.TrimPrefix(object.Key, root), "/")
		if len(parts) != 2 {
			continue // deeper nesting is not our layout and is never touched
		}
		entry := directories[parts[0]]
		if entry == nil {
			entry = &found{}
			directories[parts[0]] = entry
		}
		switch parts[1] {
		case ArchiveFile:
			entry.archive, entry.size = true, object.Size
		case ManifestFile:
			entry.manifest = true
		default:
			entry.unexpected = true
		}
	}
	out := []Entry{}
	for name, f := range directories {
		at, ok := ParseDirectorySegment(name)
		directory := root + name
		entry := Entry{Directory: directory, ManifestKey: directory + "/" + ManifestFile, ArchiveKey: directory + "/" + ArchiveFile, TakenAt: at, SizeBytes: f.size}
		switch {
		case !ok || f.unexpected:
			entry.State = StateDamaged
		case f.archive && f.manifest:
			entry.State = StateComplete
		case f.archive:
			entry.State = StateArchiveOnly
		default:
			entry.State = StateManifestOnly
		}
		out = append(out, entry)
	}
	sortEntries(out)
	return out
}

// sortEntries orders by the timestamp folder, newest first. Sorting keys as
// text instead would group by database name and hide whole databases.
func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].TakenAt.Equal(entries[j].TakenAt) {
			return entries[i].Directory > entries[j].Directory
		}
		return entries[i].TakenAt.After(entries[j].TakenAt)
	})
}

// CheckManifest applies the layout rules to a manifest's own contents. A
// manifest that disagrees with the directory holding it is damaged, never
// restored and never deleted.
func CheckManifest(m Manifest, entry Entry) error {
	if m.Version != 1 {
		return errors.New("unsupported manifest version")
	}
	if m.ManifestKey != entry.ManifestKey {
		return fmt.Errorf("manifest key %q does not match its location", m.ManifestKey)
	}
	if m.ArchiveKey != entry.ArchiveKey {
		return fmt.Errorf("archive key %q does not match its location", m.ArchiveKey)
	}
	if !ValidDatabaseSegment(m.DBName) || !strings.HasSuffix(entry.parent(), "/"+m.DBName) {
		return fmt.Errorf("database name %q does not match its location", m.DBName)
	}
	if m.CreatedAt.UTC().Format(TimeLayout) != entry.directoryName() {
		return errors.New("manifest timestamp does not match its location")
	}
	return nil
}

func (e Entry) parent() string {
	if i := strings.LastIndex(e.Directory, "/"); i > 0 {
		return e.Directory[:i]
	}
	return e.Directory
}

func (e Entry) directoryName() string {
	if i := strings.LastIndex(e.Directory, "/"); i >= 0 {
		return e.Directory[i+1:]
	}
	return e.Directory
}

// EntryFor rebuilds an entry from a manifest key so stored rows can be checked
// against the same rules as freshly listed ones.
func (c *Client) EntryFor(manifestKey string) (Entry, error) {
	root := c.key("backups") + "/"
	rest := strings.TrimPrefix(manifestKey, root)
	parts := strings.Split(rest, "/")
	if !strings.HasPrefix(manifestKey, root) || len(parts) != 3 || parts[2] != ManifestFile || !ValidDatabaseSegment(parts[0]) {
		return Entry{}, errors.New("not a backup manifest in the configured folder")
	}
	at, ok := ParseDirectorySegment(parts[1])
	if !ok {
		return Entry{}, errors.New("not a backup manifest in the configured folder")
	}
	directory := root + parts[0] + "/" + parts[1]
	return Entry{Directory: directory, ManifestKey: manifestKey, ArchiveKey: directory + "/" + ArchiveFile, TakenAt: at}, nil
}

// Remove deletes one object. Retention calls it in publication's mirror order:
// manifest, then archive, so a half-deleted backup is never discoverable.
func (c *Client) Remove(ctx context.Context, key string) error {
	if e := c.mc.RemoveObject(ctx, c.settings.Bucket, key, minio.RemoveObjectOptions{}); e != nil {
		return errors.New(describe(e))
	}
	return nil
}
