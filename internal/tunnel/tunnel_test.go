package tunnel

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestBuiltInProvidersAreRegistered(t *testing.T) {
	// Each provider registers itself in an init function, so this also checks
	// that no file was added without being wired in.
	want := map[string]bool{"auto": true, "local": true, "cloudflare": true, "relay": true}
	for _, name := range Available() {
		delete(want, name)
	}
	if len(want) != 0 {
		t.Errorf("these providers are not registered: %v", want)
	}
}

func TestOpenUnknownProviderNamesTheAlternatives(t *testing.T) {
	_, err := Open("dropbox", Config{})
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("Open returned %v, want ErrUnknownProvider", err)
	}
	// An error about a typo should tell the user what they could have typed.
	if !strings.Contains(err.Error(), "cloudflare") {
		t.Errorf("error %q does not list the available providers", err)
	}
}

func TestMissingBinaryExplainsHowToInstallIt(t *testing.T) {
	provider, err := Open("cloudflare", Config{})
	if err != nil {
		t.Fatal(err)
	}

	// Rename the binary we look for so the lookup is guaranteed to fail,
	// whether or not cloudflared is installed on the machine running tests.
	process, ok := provider.(*processTunnel)
	if !ok {
		t.Fatalf("cloudflare provider is a %T, want *processTunnel", provider)
	}
	process.spec.binary = "vrok-definitely-not-installed"

	_, err = process.Start(context.Background(), "http://127.0.0.1:8080")
	var notInstalled *ErrNotInstalled
	if !errors.As(err, &notInstalled) {
		t.Fatalf("Start returned %v, want ErrNotInstalled", err)
	}
	if notInstalled.Hint == "" {
		t.Error("the error carries no installation hint")
	}
}

func TestStopIsSafeOnATunnelThatNeverStarted(t *testing.T) {
	for _, name := range Available() {
		provider, err := Open(name, Config{Label: "abcd1234", RelayURL: "http://localhost:1"})
		if err != nil {
			t.Fatalf("Open(%q): %v", name, err)
		}
		// Callers defer Stop immediately after Start, so Stop must tolerate a
		// tunnel that failed to start.
		if err := provider.Stop(context.Background()); err != nil {
			t.Errorf("Stop on an unstarted %q tunnel returned %v", name, err)
		}
	}
}

// Stopping a provider is a request for it to exit, so whatever exit status it
// then reports is the expected outcome rather than a failure. Asking each
// platform what a terminated process looks like is a trap: Windows has no
// SIGTERM, and syscall.WaitStatus.Signaled is hard-coded false there.
func TestStopDoesNotReportTheExitItAskedFor(t *testing.T) {
	spec := processSpec{
		provider: "test",
		binary:   "sh",
		args:     func(*url.URL) []string { return []string{"-c", "echo https://x.test; sleep 60"} },
		urlPatterns: []*regexp.Regexp{
			regexp.MustCompile(`https://x\.test`),
		},
	}

	tun := newProcessTunnel(spec, Config{})
	if _, err := tun.Start(context.Background(), "http://127.0.0.1:1234"); err != nil {
		t.Skipf("cannot run a child process here: %v", err)
	}

	if err := tun.Stop(context.Background()); err != nil {
		t.Errorf("Stop after a successful start returned %v, want nil", err)
	}
	// Stop is commonly deferred as well as called explicitly.
	if err := tun.Stop(context.Background()); err != nil {
		t.Errorf("second Stop returned %v, want nil", err)
	}
}

// A provider that dies on its own has something worth reporting, and that
// should not be swallowed by the same code path that ignores a requested exit.
func TestStopReportsAProviderThatDiedOnItsOwn(t *testing.T) {
	spec := processSpec{
		provider: "test",
		binary:   "sh",
		args: func(*url.URL) []string {
			return []string{"-c", "echo https://x.test; sleep 0.2; exit 3"}
		},
		urlPatterns: []*regexp.Regexp{regexp.MustCompile(`https://x\.test`)},
	}

	tun := newProcessTunnel(spec, Config{})
	if _, err := tun.Start(context.Background(), "http://127.0.0.1:1234"); err != nil {
		t.Skipf("cannot run a child process here: %v", err)
	}

	// Give the child time to fail by itself before Stop is called.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-tun.exited:
			if err := tun.Stop(context.Background()); err == nil {
				t.Error("Stop returned nil for a provider that exited with status 3")
			}
			return
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Skip("child did not exit in time")
}

func TestPublicURLJoin(t *testing.T) {
	cases := []struct {
		base, path, want string
	}{
		{"https://abc.trycloudflare.com", "/s/token/", "https://abc.trycloudflare.com/s/token/"},
		{"https://abc.trycloudflare.com/", "/s/token/", "https://abc.trycloudflare.com/s/token/"},
		{"https://abc.trycloudflare.com", "s/token/", "https://abc.trycloudflare.com/s/token/"},
	}
	for _, tc := range cases {
		if got := PublicURL(tc.base).Join(tc.path); got != tc.want {
			t.Errorf("PublicURL(%q).Join(%q) = %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}

func TestAgentEndpointDerivation(t *testing.T) {
	cases := map[string]string{
		"https://relay.example.com":       "wss://relay.example.com/_vrok/agent",
		"http://localhost:8787":           "ws://localhost:8787/_vrok/agent",
		"wss://relay.example.com":         "wss://relay.example.com/_vrok/agent",
		"https://relay.example.com/base/": "wss://relay.example.com/base/_vrok/agent",
	}
	for input, want := range cases {
		got, err := agentEndpoint(input)
		if err != nil {
			t.Errorf("agentEndpoint(%q) returned %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("agentEndpoint(%q) = %q, want %q", input, got, want)
		}
	}

	for _, input := range []string{"", "relay.example.com", "ftp://relay.example.com", "://bad"} {
		if _, err := agentEndpoint(input); err == nil {
			t.Errorf("agentEndpoint(%q) was accepted", input)
		}
	}
}

func TestRelayProviderNeedsAShareLabel(t *testing.T) {
	// The label becomes the hostname, so there is nothing sensible to do
	// without it.
	if _, err := Open("relay", Config{RelayURL: "https://relay.example.com"}); err == nil {
		t.Error("the relay provider was built without a share label")
	}
}

func TestLocalTunnelLeavesConcreteHostsAlone(t *testing.T) {
	local, err := Open("local", Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"http://127.0.0.1:8080", "http://192.168.1.20:8080"} {
		got, err := local.Start(context.Background(), target)
		if err != nil {
			t.Fatal(err)
		}
		if got.String() != target {
			t.Errorf("Start(%q) = %q, want it unchanged", target, got)
		}
	}
}

func TestFirstRangeStartParsing(t *testing.T) {
	// Guard against a provider URL pattern matching something in its own
	// documentation output rather than the assigned hostname.
	for _, name := range []string{"cloudflare"} {
		provider, err := Open(name, Config{})
		if err != nil {
			t.Fatal(err)
		}
		process := provider.(*processTunnel)
		if len(process.spec.urlPatterns) == 0 {
			t.Errorf("%s has no URL patterns, so its public URL could never be detected", name)
		}
		for _, pattern := range process.spec.urlPatterns {
			if pattern.MatchString("see https://example.com/docs for help") {
				t.Errorf("%s pattern %q matches unrelated documentation URLs", name, pattern)
			}
		}
	}
}
