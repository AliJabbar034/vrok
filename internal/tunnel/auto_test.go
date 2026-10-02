package tunnel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A relay the user configured is theirs, so it is tried before anyone else's
// service. Without one, the hosted providers are the only candidates.
func TestConfiguredRelayIsPreferredOverHostedProviders(t *testing.T) {
	withRelay := (&autoTunnel{cfg: Config{RelayURL: "https://relay.example.com"}}).candidates()
	if len(withRelay) == 0 || withRelay[0] != "relay" {
		t.Fatalf("candidates = %v, want relay first", withRelay)
	}

	without := (&autoTunnel{}).candidates()
	for _, name := range without {
		if name == "relay" {
			t.Fatal("relay was offered with no relay URL configured, which can only fail")
		}
	}
}

// Every candidate has to be a provider that actually exists, or auto would
// skip a route for a reason that looks like "not installed".
func TestEveryCandidateIsARegisteredProvider(t *testing.T) {
	// The share id is always set by the time a tunnel is opened; the relay
	// provider needs it to claim a hostname.
	cfg := Config{RelayURL: "https://r.example.com", Label: "a82kd9", ShareToken: "tok"}

	for _, name := range (&autoTunnel{cfg: cfg}).candidates() {
		if _, err := Open(name, cfg); err != nil {
			t.Errorf("candidate %q is not usable: %v", name, err)
		}
	}
}

// Before it has chosen, auto has no route to report. Afterwards it must name
// the real one, because "auto" tells a user nothing about where their file is
// being served from.
func TestAutoReportsTheProviderItChose(t *testing.T) {
	a := &autoTunnel{}
	if got := a.Name(); got != "auto" {
		t.Errorf("Name() = %q before starting, want %q", got, "auto")
	}

	chosen, err := Open("local", Config{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	a.delegate = chosen

	if got := a.Name(); got != "local" {
		t.Errorf("Name() = %q, want the chosen provider %q", got, "local")
	}
}

// Stopping a tunnel that never chose anything is what happens whenever a
// share fails early, and callers always defer it.
func TestStoppingAnUnstartedAutoTunnelIsSafe(t *testing.T) {
	if err := (&autoTunnel{}).Stop(context.Background()); err != nil {
		t.Fatalf("Stop on an unstarted tunnel: %v", err)
	}
}

// Falling back to a loopback URL after visibly trying to go public would be a
// downgrade dressed up as success, so the failure stays a failure — while
// still carrying the causes for anyone inspecting it.
func TestNoRouteKeepsItsCauses(t *testing.T) {
	cause := errors.New("cloudflared exited")
	err := &ErrNoRoute{Causes: []error{cause}}

	if !errors.Is(err, cause) {
		t.Error("ErrNoRoute lost the failure it wrapped")
	}
	for _, want := range []string{"--local", "relay-url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error text does not mention %q, so the user is told what failed but not what to do", want)
		}
	}
}

// vrok must know which asset to fetch for the platforms it ships binaries
// for; a gap here is a user who gets no public URL at all.
func TestEveryShippedPlatformHasAProviderBuild(t *testing.T) {
	if _, _, err := cloudflaredAsset(); err != nil {
		t.Fatalf("no cloudflared build for the platform running the tests: %v", err)
	}
}

func TestProvisionerFetchesExtractsAndCaches(t *testing.T) {
	asset, tarred, err := cloudflaredAsset()
	if err != nil {
		t.Skipf("unsupported platform: %v", err)
	}

	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, asset) {
			t.Errorf("requested %q, which is not the asset for this platform", r.URL.Path)
		}
		hits++
		if tarred {
			w.Write(fakeTarball(t, "cloudflared", "#!/bin/sh\nexit 0\n"))
			return
		}
		io.WriteString(w, "#!/bin/sh\nexit 0\n")
	}))
	defer server.Close()

	t.Setenv("VROK_CACHE_DIR", t.TempDir())
	restore := downloadBase
	downloadBase = server.URL
	defer func() { downloadBase = restore }()

	p := &provisioner{logger: discardLogger()}

	path, err := p.executable(context.Background())
	if err != nil {
		t.Fatalf("executable: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		t.Error("the fetched provider is not executable")
	}

	// A second share must not pay for the download again.
	if _, err := p.executable(context.Background()); err != nil {
		t.Fatalf("second executable: %v", err)
	}
	if hits != 1 {
		t.Errorf("fetched %d times, want the cache to serve every call after the first", hits)
	}
}

// An archive naming a path outside the cache directory is how extraction
// turns into arbitrary file write, so only the expected entry is taken.
func TestProvisionerIgnoresUnexpectedArchiveEntries(t *testing.T) {
	if _, tarred, _ := cloudflaredAsset(); !tarred {
		t.Skip("this platform downloads a bare executable")
	}

	cache := t.TempDir()
	t.Setenv("VROK_CACHE_DIR", cache)

	escape := filepath.Join(cache, "escaped")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(fakeTarball(t, "../../"+filepath.Base(escape), "owned"))
	}))
	defer server.Close()

	restore := downloadBase
	downloadBase = server.URL
	defer func() { downloadBase = restore }()

	p := &provisioner{logger: discardLogger()}
	if _, err := p.executable(context.Background()); err == nil {
		t.Fatal("an archive with no cloudflared entry was accepted")
	}
	if _, err := os.Stat(escape); err == nil {
		t.Fatal("extraction wrote a file the archive named outside the cache")
	}
}

// A truncated or unreadable download must not leave a partial file that a
// later run would find cached and execute.
func TestInterruptedDownloadLeavesNothingToExecute(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("VROK_CACHE_DIR", cache)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	restore := downloadBase
	downloadBase = server.URL
	defer func() { downloadBase = restore }()

	p := &provisioner{logger: discardLogger()}
	if _, err := p.executable(context.Background()); err == nil {
		t.Fatal("a failed download reported success")
	}

	dir, err := cacheDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			t.Errorf("a failed download left %q behind", entry.Name())
		}
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func fakeTarball(t *testing.T, name, content string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	archive := tar.NewWriter(gz)

	if err := archive.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o755,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
