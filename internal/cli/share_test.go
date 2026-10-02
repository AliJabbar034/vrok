package cli

import (
	"strings"
	"testing"

	"github.com/AliJabbar034/vrok/internal/config"
)

// A loopback URL looks exactly as shareable as a public one. Describing the
// reach is the only thing standing between an owner and pasting 127.0.0.1
// into a chat, where it means the recipient's own machine.
func TestReachDescribesWhoCanOpenTheURL(t *testing.T) {
	cases := []struct {
		name       string
		tunnelKind string
		url        string
		wantReach  string
		wantHint   bool
	}{
		{
			name:       "default binds loopback",
			tunnelKind: "local",
			url:        "http://127.0.0.1:51182/s/token/",
			wantReach:  "this machine only",
			wantHint:   true,
		},
		{
			name:       "localhost by name is still loopback",
			tunnelKind: "local",
			url:        "http://localhost:8080/s/token/",
			wantReach:  "this machine only",
			wantHint:   true,
		},
		{
			name:       "IPv6 loopback is still loopback",
			tunnelKind: "local",
			url:        "http://[::1]:8080/s/token/",
			wantReach:  "this machine only",
			wantHint:   true,
		},
		{
			name:       "a LAN address reaches the network",
			tunnelKind: "local",
			url:        "http://192.168.18.66:8080/s/token/",
			wantReach:  "your local network",
			wantHint:   true,
		},
		{
			name:       "a tunnel reaches the internet",
			tunnelKind: "cloudflare",
			url:        "https://calm-wave-1234.trycloudflare.com/s/token/",
			wantReach:  "anyone with the link",
			wantHint:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &sharer{tunnelKind: tc.tunnelKind}
			reach, hint := s.reach(tc.url)

			if reach != tc.wantReach {
				t.Errorf("reach = %q, want %q", reach, tc.wantReach)
			}
			if tc.wantHint && hint == "" {
				t.Error("no hint offered for a URL that is not publicly reachable")
			}
			if !tc.wantHint && hint != "" {
				t.Errorf("a publicly reachable URL should need no hint, got %q", hint)
			}
		})
	}
}

// --local falls back to loopback on a machine with no usable network address.
// The banner is built from the address actually bound, so it reports the
// fallback rather than the intent.
func TestReachReportsTheAddressBoundNotTheFlag(t *testing.T) {
	s := &sharer{tunnelKind: "local"}

	reach, _ := s.reach("http://127.0.0.1:8080/s/token/")
	if !strings.Contains(reach, "this machine only") {
		t.Errorf("reach = %q, want it to admit the loopback fallback", reach)
	}
}

// The suggested provider comes from the registry, so adding a provider does
// not leave this message naming one that no longer exists.
func TestSuggestedProviderIsRegisteredAndUsable(t *testing.T) {
	name := firstHostedProvider()

	switch name {
	case "", "local":
		t.Fatalf("firstHostedProvider() = %q, which is not a way to go public", name)
	case "relay":
		t.Fatal("relay needs a server the user does not have yet, so it is a poor suggestion")
	}
}

// Every message that suggests storing a relay writes it as `relay-url`, which
// is also how the flag is spelled, so that is what a user types. It used to be
// rejected as an unknown setting, making the tool's own advice fail.
func TestConfigAcceptsTheKeySpellingTheFlagsUse(t *testing.T) {
	for _, key := range []string{"relay-url", "relay_url"} {
		t.Run(key, func(t *testing.T) {
			var cfg config.Config
			if err := applySetting(&cfg, key, "https://relay.example.com"); err != nil {
				t.Fatalf("applySetting(%q): %v", key, err)
			}
			if cfg.RelayURL != "https://relay.example.com" {
				t.Errorf("RelayURL = %q, want the value to have been stored", cfg.RelayURL)
			}
		})
	}
}

// A mistyped key should say what the real ones are rather than leaving the
// user to guess at the separator or the spelling.
func TestUnknownSettingNamesTheRealOnes(t *testing.T) {
	err := applySetting(&config.Config{}, "relayurl", "x")
	if err == nil {
		t.Fatal("applySetting accepted a key that does not exist")
	}
	if !strings.Contains(err.Error(), "relay_url") {
		t.Errorf("error %q does not list the available settings", err)
	}
}
