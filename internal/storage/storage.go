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
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Bucket protection: backups are only as safe as the bucket holding them. The
// application credential can write and delete objects, so either the provider
// keeps versions of what it deletes, or the operator says explicitly that they
// accept that risk.
const (
	ProtectionVersioning   = "versioning"
	ProtectionAcknowledged = "acknowledged"
)

// Observed states of the bucket's versioning configuration.
const (
	VersioningEnabled     = "enabled"
	VersioningDisabled    = "disabled"
	VersioningUnsupported = "unsupported"
)

type Settings struct {
	Endpoint            string `json:"endpoint"`
	Region              string `json:"region"`
	Bucket              string `json:"bucket"`
	Prefix              string `json:"prefix"`
	AccessKey           string `json:"access_key"`
	SecretKey           string `json:"secret_key"`
	SessionToken        string `json:"session_token"`
	PathStyle           bool   `json:"path_style"`
	PrivateEndpoint     bool   `json:"private_endpoint"`
	BucketProtection    string `json:"bucket_protection"`
	ProtectionState     string `json:"protection_state"`
	ProtectionCheckedAt int64  `json:"protection_checked_at"`
}

// Target identifies one store. Cached knowledge of a bucket is keyed by it, so
// changing endpoint, bucket or folder starts a fresh view instead of mixing two.
func (s Settings) Target() string {
	return Hash(strings.ToLower(s.Endpoint) + "|" + s.Bucket + "|" + strings.Trim(s.Prefix, "/"))
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
	if u.Scheme != "https" {
		if !s.PrivateEndpoint {
			return errors.New("the endpoint must use https:// unless it is a private endpoint on this network")
		}
		if e := privateHost(u.Hostname()); e != nil {
			return e
		}
	}
	if s.BucketProtection != ProtectionVersioning && s.BucketProtection != ProtectionAcknowledged {
		return errors.New("choose how this bucket is protected against deletion")
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
	options := &minio.Options{
		Creds:        credentials.NewStaticV4(s.AccessKey, s.SecretKey, s.SessionToken),
		Secure:       u.Scheme == "https",
		Region:       region,
		BucketLookup: lookup,
	}
	if s.PrivateEndpoint {
		// Resolving the name once at save time would still allow a later answer
		// to point somewhere public, so the address actually dialled is checked.
		options.Transport = privateTransport()
	}
	mc, e := minio.New(u.Host, options)
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
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
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
	steps = append(steps, CheckStep{Name: "cleanup", OK: true})
	state, e := c.Protection(ctx)
	if e != nil {
		return fail("protection", e)
	}
	step := CheckStep{Name: "protection", OK: true}
	switch state {
	case VersioningEnabled:
		step.Detail = "the bucket keeps versions of deleted objects"
	case VersioningDisabled:
		step.OK, step.Error = false, "versioning is off for this bucket; a deleted backup cannot be recovered"
	default:
		step.Detail = "this provider does not report versioning; protection is the acknowledged setting"
	}
	return append(steps, step)
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

// privateHost refuses a plaintext endpoint that is not on this machine or a
// private network.
func privateHost(host string) error {
	addresses, e := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if e != nil || len(addresses) == 0 {
		return errors.New("the private endpoint's address could not be resolved")
	}
	for _, a := range addresses {
		if !privateAddress(a.IP) {
			return errors.New("a private endpoint must be a loopback or private network address")
		}
	}
	return nil
}

func privateAddress(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func privateTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		host, _, e := net.SplitHostPort(address)
		if e != nil {
			return errors.New("the private endpoint address could not be read")
		}
		ip := net.ParseIP(host)
		if ip == nil || !privateAddress(ip) {
			return errors.New("the private endpoint resolved to a public address and was not contacted")
		}
		return nil
	}}
	return &http.Transport{DialContext: dialer.DialContext, ForceAttemptHTTP2: true, MaxIdleConns: 16, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second}
}

// Protection reports what the provider says about the bucket keeping versions
// of deleted objects. Anything other than a clear answer fails closed: an
// access-denied reply may be a supported API hidden by a narrow credential.
func (c *Client) Protection(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	configuration, e := c.mc.GetBucketVersioning(ctx, c.settings.Bucket)
	if e == nil {
		if configuration.Enabled() {
			return VersioningEnabled, nil
		}
		return VersioningDisabled, nil
	}
	var response minio.ErrorResponse
	if errors.As(e, &response) {
		switch response.Code {
		case "NotImplemented", "MethodNotAllowed", "NotSupported", "UnsupportedOperation":
			return VersioningUnsupported, nil
		case "AccessDenied":
			return "", errors.New("the credentials may not read the bucket's versioning setting; allow s3:GetBucketVersioning and try again")
		}
	}
	return "", errors.New("the bucket's versioning setting could not be read: " + describe(e))
}

func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type ObjectCounts struct {
	Sequences   int64 `json:"sequences"`
	Views       int64 `json:"views"`
	Functions   int64 `json:"functions"`
	Indexes     int64 `json:"indexes"`
	Constraints int64 `json:"constraints"`
}

type TableCount struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Rows   int64  `json:"rows"`
}

// Manifest is published last; a backup without one is incomplete and ignored.
type Manifest struct {
	Version         int           `json:"version"`
	InstallationID  string        `json:"installation_id"`
	ProjectID       string        `json:"project_id"`
	ProjectName     string        `json:"project_name"`
	DBName          string        `json:"db_name"`
	PostgresVersion string        `json:"postgres_version"`
	CreatedAt       time.Time     `json:"created_at"`
	ArchiveKey      string        `json:"archive_key"`
	SHA256          string        `json:"sha256"`
	SizeBytes       int64         `json:"size_bytes"`
	Tables          []TableCount  `json:"tables"`
	TablesTruncated bool          `json:"tables_truncated,omitempty"`
	Objects         *ObjectCounts `json:"objects,omitempty"`
	ManifestKey     string        `json:"manifest_key,omitempty"`
}

// Manifest reads and fully validates one manifest. The restore path uses the
// same layout rules as listing and retention, so a manifest that contradicts
// where it lives can never be restored.
func (c *Client) Manifest(ctx context.Context, key string) (Manifest, error) {
	var m Manifest
	entry, e := c.EntryFor(key)
	if e != nil {
		return m, e
	}
	b, e := c.ReadBytes(ctx, key, 1<<20)
	if e != nil {
		return m, e
	}
	if e := json.Unmarshal(b, &m); e != nil {
		return m, errors.New("the manifest is unreadable or from an unsupported version")
	}
	m.ManifestKey = key
	if e := CheckManifest(m, entry); e != nil {
		return m, errors.New("the backup's manifest does not match its location and will not be restored")
	}
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
