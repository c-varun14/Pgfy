// Package storage is the single S3-compatible client used for every backup
// store. The operator supplies an existing private bucket and scoped keys;
// nothing here provisions buckets, IAM, or provider-specific features.
package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Settings struct {
	Endpoint     string `json:"endpoint"`
	Region       string `json:"region"`
	Bucket       string `json:"bucket"`
	Prefix       string `json:"prefix"`
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key"`
	SessionToken string `json:"session_token"`
	PathStyle    bool   `json:"path_style"`
}

// Masked hides credentials for display while showing that they are set.
func (s Settings) Masked() Settings {
	mask := func(v string) string {
		if v == "" {
			return ""
		}
		if len(v) > 4 {
			return "••••" + v[len(v)-4:]
		}
		return "••••"
	}
	s.AccessKey = mask(s.AccessKey)
	s.SecretKey = mask(s.SecretKey)
	s.SessionToken = mask(s.SessionToken)
	return s
}

func (s Settings) Validate() error {
	u, e := url.Parse(s.Endpoint)
	if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Path != "" && u.Path != "/" {
		return errors.New("endpoint must be an https:// URL without a path, for example https://s3.us-east-1.amazonaws.com")
	}
	if s.Bucket == "" || strings.ContainsAny(s.Bucket, "/ ") {
		return errors.New("bucket name is required")
	}
	if s.AccessKey == "" || s.SecretKey == "" {
		return errors.New("access key and secret key are required")
	}
	if strings.HasPrefix(s.Prefix, "/") || strings.Contains(s.Prefix, "..") {
		return errors.New("prefix must be a relative path such as pgfy/production")
	}
	return nil
}

type Client struct {
	settings Settings
	mc       *minio.Client
}

func New(s Settings) (*Client, error) {
	if e := s.Validate(); e != nil {
		return nil, e
	}
	u, _ := url.Parse(s.Endpoint)
	lookup := minio.BucketLookupDNS
	if s.PathStyle {
		lookup = minio.BucketLookupPath
	}
	region := s.Region
	if region == "" {
		region = "us-east-1"
	}
	mc, e := minio.New(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(s.AccessKey, s.SecretKey, s.SessionToken),
		Secure:       u.Scheme == "https",
		Region:       region,
		BucketLookup: lookup,
	})
	if e != nil {
		return nil, e
	}
	return &Client{settings: s, mc: mc}, nil
}

func (c *Client) key(parts ...string) string {
	return path.Join(append([]string{strings.Trim(c.settings.Prefix, "/")}, parts...)...)
}

// BackupKey is where one backup's objects live: <prefix>/backups/<db>/<time>/.
func (c *Client) BackupKey(dbName string, at time.Time) string {
	return c.key("backups", dbName, at.UTC().Format("20060102T150405Z"))
}

type CheckStep struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Check exercises upload, listing, download with integrity, and cleanup of a
// temporary object so a misconfigured bucket is found before the first backup.
func (c *Client) Check(ctx context.Context) []CheckStep {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	key := c.key("checks", fmt.Sprintf("pgfy-check-%d.txt", time.Now().UnixNano()))
	payload := []byte("pgfy storage check " + time.Now().UTC().Format(time.RFC3339))
	steps := []CheckStep{}
	fail := func(name string, e error) []CheckStep {
		steps = append(steps, CheckStep{Name: name, Error: describe(e)})
		return steps
	}
	if _, e := c.mc.PutObject(ctx, c.settings.Bucket, key, bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{ContentType: "text/plain"}); e != nil {
		return fail("upload", e)
	}
	steps = append(steps, CheckStep{Name: "upload", OK: true})
	found := false
	for object := range c.mc.ListObjects(ctx, c.settings.Bucket, minio.ListObjectsOptions{Prefix: key}) {
		if object.Err != nil {
			return fail("list", object.Err)
		}
		if object.Key == key {
			found = true
		}
	}
	if !found {
		return fail("list", errors.New("the uploaded object was not listed"))
	}
	steps = append(steps, CheckStep{Name: "list", OK: true})
	object, e := c.mc.GetObject(ctx, c.settings.Bucket, key, minio.GetObjectOptions{})
	if e == nil {
		var got []byte
		got, e = io.ReadAll(object)
		object.Close()
		if e == nil && !bytes.Equal(got, payload) {
			e = errors.New("downloaded content differs from the upload")
		}
	}
	if e != nil {
		return fail("download", e)
	}
	steps = append(steps, CheckStep{Name: "download", OK: true})
	if e := c.mc.RemoveObject(ctx, c.settings.Bucket, key, minio.RemoveObjectOptions{}); e != nil {
		return fail("cleanup", e)
	}
	return append(steps, CheckStep{Name: "cleanup", OK: true})
}

func describe(e error) string {
	var response minio.ErrorResponse
	if errors.As(e, &response) && response.Code != "" {
		switch response.Code {
		case "AccessDenied":
			return "Access denied: the credentials cannot perform this operation on the bucket."
		case "NoSuchBucket":
			return "The bucket does not exist at this endpoint/region."
		case "InvalidAccessKeyId", "SignatureDoesNotMatch":
			return "The access key or secret key is not accepted by the endpoint."
		}
		return response.Code + ": " + response.Message
	}
	return e.Error()
}

// UploadFile streams a local file; multipart is handled by the client.
func (c *Client) UploadFile(ctx context.Context, key, file string) error {
	_, e := c.mc.FPutObject(ctx, c.settings.Bucket, key, file, minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if e != nil {
		return errors.New(describe(e))
	}
	return nil
}

func (c *Client) UploadBytes(ctx context.Context, key string, b []byte, contentType string) error {
	_, e := c.mc.PutObject(ctx, c.settings.Bucket, key, bytes.NewReader(b), int64(len(b)), minio.PutObjectOptions{ContentType: contentType})
	if e != nil {
		return errors.New(describe(e))
	}
	return nil
}

func (c *Client) DownloadFile(ctx context.Context, key, file string) error {
	if e := c.mc.FGetObject(ctx, c.settings.Bucket, key, file, minio.GetObjectOptions{}); e != nil {
		return errors.New(describe(e))
	}
	return nil
}

func (c *Client) ReadBytes(ctx context.Context, key string, limit int64) ([]byte, error) {
	object, e := c.mc.GetObject(ctx, c.settings.Bucket, key, minio.GetObjectOptions{})
	if e != nil {
		return nil, errors.New(describe(e))
	}
	defer object.Close()
	b, e := io.ReadAll(io.LimitReader(object, limit))
	if e != nil {
		return nil, errors.New(describe(e))
	}
	return b, nil
}

type TableCount struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Rows   int64  `json:"rows"`
}

// Manifest is published last; a backup without one is incomplete and ignored.
type Manifest struct {
	Version         int          `json:"version"`
	InstallationID  string       `json:"installation_id"`
	ProjectID       string       `json:"project_id"`
	ProjectName     string       `json:"project_name"`
	DBName          string       `json:"db_name"`
	PostgresVersion string       `json:"postgres_version"`
	CreatedAt       time.Time    `json:"created_at"`
	ArchiveKey      string       `json:"archive_key"`
	SHA256          string       `json:"sha256"`
	SizeBytes       int64        `json:"size_bytes"`
	Tables          []TableCount `json:"tables"`
	ManifestKey     string       `json:"manifest_key,omitempty"`
}

// ListManifests discovers completed backups under the prefix, newest first.
func (c *Client) ListManifests(ctx context.Context, limit int) ([]Manifest, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var keys []string
	for object := range c.mc.ListObjects(ctx, c.settings.Bucket, minio.ListObjectsOptions{Prefix: c.key("backups") + "/", Recursive: true}) {
		if object.Err != nil {
			return nil, errors.New(describe(object.Err))
		}
		if strings.HasSuffix(object.Key, "/manifest.json") {
			keys = append(keys, object.Key)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	if len(keys) > limit {
		keys = keys[:limit]
	}
	out := []Manifest{}
	for _, key := range keys {
		b, e := c.ReadBytes(ctx, key, 1<<20)
		if e != nil {
			return nil, e
		}
		var m Manifest
		if e := json.Unmarshal(b, &m); e != nil || m.Version != 1 {
			continue // foreign or damaged manifests are not offered for restore
		}
		m.ManifestKey = key
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (c *Client) Manifest(ctx context.Context, key string) (Manifest, error) {
	var m Manifest
	if !strings.HasSuffix(key, "/manifest.json") || !strings.HasPrefix(key, c.key("backups")+"/") {
		return m, errors.New("not a backup manifest in the configured prefix")
	}
	b, e := c.ReadBytes(ctx, key, 1<<20)
	if e != nil {
		return m, e
	}
	if e := json.Unmarshal(b, &m); e != nil || m.Version != 1 {
		return m, errors.New("the manifest is unreadable or from an unsupported version")
	}
	m.ManifestKey = key
	return m, nil
}

func FileSHA256(file string) (string, int64, error) {
	f, e := os.Open(file)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	if e != nil {
		return "", 0, e
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
