package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AliJabbar034/vrok/internal/ui"
)

// doctorEnv isolates doctor from this machine: its own config, state and
// cache directories, a stub Cloudflare, and a fixed newest release.
type doctorEnv struct {
	configDir string
	latest    string
	latestErr error
	cfUp      bool
}

func runDoctorIn(t *testing.T, env doctorEnv, current string) (string, bool) {
	t.Helper()
	root := t.TempDir()
	if env.configDir == "" {
		env.configDir = filepath.Join(root, "config")
	}
	t.Setenv("XDG_CONFIG_HOME", env.configDir)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("VROK_CACHE_DIR", filepath.Join(root, "cache"))
	t.Setenv("PATH", root) // no vrok, no cloudflared
	t.Setenv("NO_COLOR", "1")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound) // any answer counts as reachable
	}))
	probe := srv.URL
	if !env.cfUp {
		srv.Close() // a closed server refuses connections, like a blocked network
	} else {
		t.Cleanup(srv.Close)
	}

	oldProbe, oldLatest := cloudflareProbe, latestRelease
	cloudflareProbe = probe
	latestRelease = func(context.Context, *http.Client) (string, error) { return env.latest, env.latestErr }
	t.Cleanup(func() { cloudflareProbe, latestRelease = oldProbe, oldLatest })

	var out bytes.Buffer
	failed := runDoctor(context.Background(), ui.New(&out, &out), current)
	return out.String(), failed
}

func TestDoctorHealthyMachine(t *testing.T) {
	out, failed := runDoctorIn(t, doctorEnv{latest: "v0.6.0", cfUp: true}, "0.6.0")
	if failed {
		t.Fatalf("a healthy machine failed:\n%s", out)
	}
	for _, want := range []string{
		"✓ Config       defaults (no config file)",
		"✓ cloudflared  not installed; the first public share downloads it",
		"✓ Shares       none running",
		"✓ Cloudflare   reachable",
		"✓ Version      newest release",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestDoctorFailsOnABrokenConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "vrok"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vrok", "config.json"), []byte("{ttl: "), 0o600); err != nil {
		t.Fatal(err)
	}

	out, failed := runDoctorIn(t, doctorEnv{configDir: dir, latest: "v0.6.0", cfUp: true}, "0.6.0")
	if !failed {
		t.Fatalf("a config that does not parse passed:\n%s", out)
	}
	if !strings.Contains(out, "✗ Config") || !strings.Contains(out, "delete it to go back to the defaults") {
		t.Errorf("the failure does not say what to do:\n%s", out)
	}
}

func TestDoctorFailsWhenCloudflareIsBlocked(t *testing.T) {
	out, failed := runDoctorIn(t, doctorEnv{latest: "v0.6.0", cfUp: false}, "0.6.0")
	if !failed {
		t.Fatalf("an unreachable Cloudflare passed:\n%s", out)
	}
	if !strings.Contains(out, "✗ Cloudflare   unreachable") || !strings.Contains(out, "--local") {
		t.Errorf("the failure does not offer --local:\n%s", out)
	}
}

func TestDoctorPointsAnOutdatedInstallAtUpdate(t *testing.T) {
	out, failed := runDoctorIn(t, doctorEnv{latest: "v0.7.0", cfUp: true}, "0.6.0")
	if failed {
		t.Fatalf("being behind is a warning, not a failure:\n%s", out)
	}
	if !strings.Contains(out, "! Version      0.7.0 is available") {
		t.Errorf("no update warning:\n%s", out)
	}
	// The test binary lives in a temp dir, so it reads as a self-managed install.
	if !strings.Contains(out, "vrok update") {
		t.Errorf("the warning does not say how to update:\n%s", out)
	}
}

func TestDoctorTreatsAnOfflineVersionCheckAsAWarning(t *testing.T) {
	out, failed := runDoctorIn(t, doctorEnv{latestErr: errors.New("no route to host"), cfUp: true}, "0.6.0")
	if failed {
		t.Fatalf("not reaching GitHub failed the whole check:\n%s", out)
	}
	if !strings.Contains(out, "! Version      could not check") {
		t.Errorf("no warning for the failed check:\n%s", out)
	}
}

func TestOnPathFindsEveryCopyOnceInOrder(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, binaryFileName()), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	empty := filepath.Join(root, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}

	dirs := []string{first, empty, first, second}
	// A symlinked directory reaching the first copy again is still one
	// install. Windows needs privileges for symlinks, so it checks the rest.
	if runtime.GOOS != "windows" {
		alias := filepath.Join(root, "alias")
		if err := os.Symlink(first, alias); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, alias)
	}
	t.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator)))

	got := onPath(binaryFileName())
	want := []string{filepath.Join(first, binaryFileName()), filepath.Join(second, binaryFileName())}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("onPath = %v, want %v", got, want)
	}
}
