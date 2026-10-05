package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/sso"
)

// JSON avoids a second configuration parser; credentials are not part of this file.
type Config struct {
	SSO                    sso.Config `json:"sso"`
	WebsitePublicRead      bool       `json:"website_public_read"`
	WebsitePublicPrepare   bool       `json:"website_public_prepare"` // deprecated: anonymous mutations are always denied
	MongoDBURI             string     `json:"database_url"`
	ListenHost             string     `json:"listen_host"`
	Port                   int        `json:"port"`
	DataRoot               string     `json:"data_root"`
	Site                   string     `json:"site"`
	RequestTimeoutSeconds  int        `json:"request_timeout_seconds"`
	ShutdownTimeoutSeconds int        `json:"shutdown_timeout_seconds"`
	MaxBodyBytes           int64      `json:"max_body_bytes"`
}

func Defaults() Config {
	return Config{WebsitePublicRead: true, ListenHost: "::", Port: 8080, DataRoot: "/data", Site: "default", RequestTimeoutSeconds: 30, ShutdownTimeoutSeconds: 15, MaxBodyBytes: 65536}
}
func Load(path string) (Config, error) {
	c := Defaults()
	f, err := os.Open(path)
	if err != nil {
		return c, fmt.Errorf("read service configuration: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return c, fmt.Errorf("read service configuration: %w", err)
	}
	if len(data) > 65536 {
		return c, fmt.Errorf("configuration exceeds 64 KiB")
	}
	if err = contract.DecodeObject(data, &c); err != nil {
		return c, fmt.Errorf("invalid configuration: %w", err)
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if e := c.SSO.Validate(); e != nil {
		return e
	}
	if c.ListenHost != "127.0.0.1" && c.ListenHost != "0.0.0.0" && c.ListenHost != "::1" && c.ListenHost != "::" {
		return fmt.Errorf("listen_host must be a supported bind address")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid port")
	}
	if !filepath.IsAbs(c.DataRoot) || filepath.Clean(c.DataRoot) == "/" {
		return fmt.Errorf("data_root must be an absolute non-root directory")
	}
	if c.RequestTimeoutSeconds < 1 || c.RequestTimeoutSeconds > 120 {
		return fmt.Errorf("invalid request timeout")
	}
	if c.ShutdownTimeoutSeconds < 1 || c.ShutdownTimeoutSeconds > 120 {
		return fmt.Errorf("invalid shutdown timeout")
	}
	if c.MaxBodyBytes < 1 || c.MaxBodyBytes > 65536 {
		return fmt.Errorf("max_body_bytes must be in 1..65536")
	}
	return nil
}
func (c Config) RequestTimeout() time.Duration {
	return time.Duration(c.RequestTimeoutSeconds) * time.Second
}
