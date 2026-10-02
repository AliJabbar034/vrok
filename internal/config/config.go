// Package config holds vrok's persistent defaults. The file is optional:
// vrok works with no configuration at all, and the file exists only so a user
// does not have to retype the flags they always use.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Defaults that apply when nothing is configured.
const (
	// DefaultTTL is how long a share lives when no --ttl is given. Zero means
	// no expiry: the URL works until the process is stopped.
	DefaultTTL time.Duration = 0
	// DefaultAddr binds to loopback on a free port. Loopback is the safe
	// default: `--local` is what opens the share to the network.
	DefaultAddr = "127.0.0.1:0"
	// DefaultTunnel picks the best available route to the internet. Sharing
	// is the point of the tool, and a link the recipient cannot open is not a
	// share, so the default produces a public URL. `--local` and
	// `--tunnel local` opt back out.
	DefaultTunnel = "auto"
)

// Config is the user's stored defaults.
type Config struct {
	// TTL is the default share lifetime.
	TTL Duration `json:"ttl"`
	// Tunnel is the default provider name for --tunnel.
	Tunnel string `json:"tunnel"`
	// RelayURL is the relay the "relay" provider connects to.
	RelayURL string `json:"relay_url,omitempty"`
	// RelayToken authenticates against a private relay.
	RelayToken string `json:"relay_token,omitempty"`
	// Addr is the local listen address.
	Addr string `json:"addr"`
	// Domain requests a custom domain where the provider supports it.
	Domain string `json:"domain,omitempty"`
	// QR prints a QR code for every share.
	QR bool `json:"qr,omitempty"`
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		TTL:    Duration(DefaultTTL),
		Tunnel: DefaultTunnel,
		Addr:   DefaultAddr,
	}
}

// Load reads the configuration file, falling back to defaults for anything it
// does not set. A missing file is not an error: it is the normal case.
func Load() (Config, error) {
	cfg := Default()

	path, err := Path()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	}
	// Unmarshalling onto the defaults leaves unset fields at their default
	// value, so a partial config file behaves as users expect.
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), fmt.Errorf("config: parse %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes the configuration file, creating its directory if needed.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: create %s: %w", filepath.Dir(path), err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	// The file can hold a relay token, so it is owner-readable only. Writing
	// to a temporary file and renaming keeps a crash from truncating it.
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", temp, err)
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
		return fmt.Errorf("config: replace %s: %w", path, err)
	}
	return nil
}

// Path returns the configuration file location.
//
// XDG_CONFIG_HOME is honoured on every platform, including macOS, where
// os.UserConfigDir would otherwise insist on ~/Library/Application Support.
// A command-line tool belongs where the rest of a user's dotfiles are.
func Path() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		var err error
		if dir, err = os.UserConfigDir(); err != nil {
			return "", fmt.Errorf("config: locate config directory: %w", err)
		}
	}
	return filepath.Join(dir, "vrok", "config.json"), nil
}
