package config_test

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/AliJabbar034/vrok/internal/config"
)

func TestParseTTL(t *testing.T) {
	cases := map[string]time.Duration{
		"30s":   30 * time.Second,
		"30m":   30 * time.Minute,
		"2h":    2 * time.Hour,
		"1d":    24 * time.Hour,
		"1.5d":  36 * time.Hour,
		"2w":    14 * 24 * time.Hour,
		"1h30m": 90 * time.Minute,
		"0":     0,
		"never": 0,
		"":      0,
		"2H":    2 * time.Hour, // case-insensitive
	}
	for input, want := range cases {
		got, err := config.ParseTTL(input)
		if err != nil {
			t.Errorf("ParseTTL(%q) returned %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("ParseTTL(%q) = %v, want %v", input, got, want)
		}
	}

	for _, input := range []string{"banana", "-1h", "-2d", "2 hours", "h"} {
		if got, err := config.ParseTTL(input); err == nil {
			t.Errorf("ParseTTL(%q) = %v, want an error", input, got)
		}
	}
}

func TestFormatTTLRoundTrips(t *testing.T) {
	for _, text := range []string{"30m", "2h", "1d", "45s"} {
		parsed, err := config.ParseTTL(text)
		if err != nil {
			t.Fatal(err)
		}
		if got := config.FormatTTL(parsed); got != text {
			t.Errorf("FormatTTL(ParseTTL(%q)) = %q", text, got)
		}
	}
	if got := config.FormatTTL(0); got != "never" {
		t.Errorf("FormatTTL(0) = %q, want never", got)
	}
}

func TestDurationJSON(t *testing.T) {
	type holder struct {
		TTL config.Duration `json:"ttl"`
	}

	// Strings are the canonical form, so the config file stays readable.
	encoded, err := json.Marshal(holder{TTL: config.Duration(90 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"ttl":"1h30m0s"}` && string(encoded) != `{"ttl":"90m"}` {
		t.Errorf("marshalled as %s, want a human-readable duration string", encoded)
	}

	var decoded holder
	if err := json.Unmarshal([]byte(`{"ttl":"45m"}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if time.Duration(decoded.TTL) != 45*time.Minute {
		t.Errorf("decoded %v, want 45m", time.Duration(decoded.TTL))
	}

	// Seconds as a number are accepted too, for anything generating the file.
	if err := json.Unmarshal([]byte(`{"ttl":120}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if time.Duration(decoded.TTL) != 2*time.Minute {
		t.Errorf("decoded %v, want 2m", time.Duration(decoded.TTL))
	}

	if err := json.Unmarshal([]byte(`{"ttl":"banana"}`), &decoded); err == nil {
		t.Error("an invalid duration string was accepted")
	}
}

func TestLoadAndSaveRoundTrip(t *testing.T) {
	// Redirect the config location so the test never touches the real one.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "config.json" {
		t.Errorf("config path = %q, want it to end in config.json", path)
	}

	// A missing file is the normal case and must not be an error.
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("Load with no file returned %v", err)
	}
	if time.Duration(loaded.TTL) != config.DefaultTTL || loaded.Tunnel != config.DefaultTunnel {
		t.Errorf("defaults were not applied: %+v", loaded)
	}

	loaded.TTL = config.Duration(30 * time.Minute)
	loaded.Tunnel = "cloudflare"
	loaded.QR = true
	if err := config.Save(loaded); err != nil {
		t.Fatalf("Save: %v", err)
	}

	again, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(again.TTL) != 30*time.Minute || again.Tunnel != "cloudflare" || !again.QR {
		t.Errorf("round trip lost settings: %+v", again)
	}
}

func TestPartialConfigKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	path, err := config.Path()
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, path, `{"tunnel":"cloudflare"}`)

	loaded, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Tunnel != "cloudflare" {
		t.Errorf("Tunnel = %q, want cloudflare", loaded.Tunnel)
	}
	// Unset keys must keep their defaults rather than becoming zero values.
	if time.Duration(loaded.TTL) != config.DefaultTTL {
		t.Errorf("TTL = %v, want the default %v", time.Duration(loaded.TTL), config.DefaultTTL)
	}
	if loaded.Addr != config.DefaultAddr {
		t.Errorf("Addr = %q, want the default %q", loaded.Addr, config.DefaultAddr)
	}
}
