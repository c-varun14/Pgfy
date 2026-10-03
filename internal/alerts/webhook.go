package alerts

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Webhook is where alerts go. The URL itself is often the credential (Slack
// and Discord embed a token in it), so it is sealed at rest and never logged,
// audited or returned in full.
type Webhook struct {
	URL             string `json:"url"`
	Secret          string `json:"secret"`
	PrivateEndpoint bool   `json:"private_endpoint"`
}

func (w Webhook) Validate() error {
	if len(w.URL) > 2048 || len(w.Secret) > 256 {
		return errors.New("the webhook URL or secret is too long")
	}
	u, e := url.Parse(w.URL)
	if e != nil || u.Host == "" || u.Hostname() == "" {
		return errors.New("enter a full webhook URL, for example https://hooks.example.com/…")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !w.PrivateEndpoint {
			return errors.New("the webhook must use https unless it is a private endpoint on this network")
		}
	default:
		return errors.New("the webhook must use https")
	}
	return nil
}

// Masked shows where alerts go without the path or query that may carry a token.
func (w Webhook) Masked() string {
	u, e := url.Parse(w.URL)
	if e != nil || u.Host == "" {
		return ""
	}
	masked := u.Scheme + "://" + u.Host
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		masked += "/…"
	}
	return masked
}

// Host is the only part of the URL that goes into the audit trail.
func (w Webhook) Host() string {
	u, e := url.Parse(w.URL)
	if e != nil {
		return ""
	}
	return u.Hostname()
}

func network(cidr string) *net.IPNet {
	_, n, _ := net.ParseCIDR(cidr)
	return n
}

var cgnat = network("100.64.0.0/10")

// Never contacted: "this network", IETF protocol assignments and benchmarking ranges.
var reserved = []*net.IPNet{network("0.0.0.0/8"), network("192.0.0.0/24"), network("198.18.0.0/15")}

// Translation prefixes carry an IPv4 address; it is what is really reached.
var nat64, sixToFour = network("64:ff9b::/96"), network("2002::/16")

// allowed decides by the address actually dialled: a public endpoint may not
// reach this machine or a private network, a private one may reach only those,
// and neither may reach link-local addresses such as the cloud metadata service.
func allowed(ip net.IP, private bool) bool {
	switch {
	case nat64.Contains(ip):
		return allowed(ip[12:16], private)
	case sixToFour.Contains(ip):
		return allowed(ip[2:6], private)
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, n := range reserved {
		if n.Contains(ip) {
			return false
		}
	}
	internal := ip.IsLoopback() || ip.IsPrivate() || cgnat.Contains(ip)
	return internal == private
}

func client(private bool, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout, Control: func(network, address string, _ syscall.RawConn) error {
		host, _, e := net.SplitHostPort(address)
		ip := net.ParseIP(host)
		if e != nil || ip == nil || !allowed(ip, private) {
			if private {
				return errors.New("a private webhook must be on this machine or a private network")
			}
			return errors.New("a public webhook may not point at this machine or a private network; use the private endpoint setting")
		}
		return nil
	}}
	// Proxy stays nil: environment proxies would bypass the address check.
	transport := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: timeout, ResponseHeaderTimeout: timeout, DisableKeepAlives: true}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Sign is what a receiver recomputes to trust a delivery: HMAC-SHA256 over
// the timestamp, a dot, and the body.
func Sign(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10) + "."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Deliver posts one message. Errors never contain the URL: Go's url.Error
// quotes it in full, so only the inner cause or the status code is kept.
func Deliver(ctx context.Context, w Webhook, body []byte, version string, now time.Time) error {
	request, e := http.NewRequestWithContext(ctx, "POST", w.URL, bytes.NewReader(body))
	if e != nil {
		return errors.New("the webhook URL is not valid")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "pgfy/"+version)
	if w.Secret != "" {
		request.Header.Set("X-Pgfy-Timestamp", strconv.FormatInt(now.Unix(), 10))
		request.Header.Set("X-Pgfy-Signature", Sign(w.Secret, now.Unix(), body))
	}
	response, e := client(w.PrivateEndpoint, 10*time.Second).Do(request)
	if e != nil {
		var ue *url.Error
		if errors.As(e, &ue) {
			e = ue.Err
		}
		return errors.New(scrub(e.Error(), w.URL))
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return &Rejected{Status: response.StatusCode}
	}
	return nil
}

// Rejected is a receiver's non-2xx answer.
type Rejected struct{ Status int }

func (r *Rejected) Error() string { return fmt.Sprintf("the receiver answered HTTP %d", r.Status) }

// scrub removes the URL, and its path, from an error text as a second line of defence.
func scrub(text, raw string) string {
	text = strings.ReplaceAll(text, raw, "(webhook)")
	if u, e := url.Parse(raw); e == nil && len(u.Path) > 1 {
		text = strings.ReplaceAll(text, u.Path, "/…")
	}
	return text
}
