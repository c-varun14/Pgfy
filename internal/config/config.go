package config

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"regexp"
)

type Installation struct {
	ID             string `json:"id"`
	Mode           string `json:"mode"`
	Origin         string `json:"origin"`
	Hostname       string `json:"hostname"`
	Generation     string `json:"generation"`
	Release        string `json:"release"`
	CaddyVersion   string `json:"caddy_version"`
	DockerVersion  string `json:"docker_version"`
	ComposeVersion string `json:"compose_version"`
}

var hostname = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

func (c Installation) Validate() error {
	u, e := url.Parse(c.Origin)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || c.ID == "" || c.Generation == "" {
		return errors.New("invalid installation configuration")
	}
	if c.Mode == "https" && hostname.MatchString(c.Hostname) && len(c.Hostname) <= 253 && u.Scheme == "https" && u.Host == c.Hostname {
		return nil
	}
	if c.Mode == "tunnel" && c.Hostname == "" && c.Origin == "http://127.0.0.1:8080" {
		return nil
	}
	return errors.New("invalid access mode or origin")
}

func Load(path string) (Installation, error) {
	var c Installation
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(b, &c); e != nil {
		return c, e
	}
	return c, c.Validate()
}

func Env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
