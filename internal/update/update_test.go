package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v0.5.2", "0.5.1", true},
		{"v0.6.0", "0.5.9", true},
		{"v1.0.0", "0.99.99", true},
		{"v0.5.1", "0.5.1", false},
		{"v0.5.1", "0.5.2", false},
		// A snapshot build is ahead of the release it was cut from.
		{"v0.5.1", "0.5.2-next", false},
		// Numeric, not string, comparison: 10 > 9.
		{"v0.10.0", "0.9.0", true},
	}
	for _, c := range cases {
		got, err := Newer(c.latest, c.current)
		if err != nil {
			t.Errorf("Newer(%q, %q): %v", c.latest, c.current, err)
			continue
		}
		if got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}

	if _, err := Newer("v0.5.1", "dev"); !errors.Is(err, ErrNotARelease) {
		t.Errorf("a dev build compared as a release: %v", err)
	}
}

func TestDetect(t *testing.T) {
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", filepath.FromSlash("/home/ana/gopath"))

	cases := map[string]Method{
		"/opt/homebrew/Caskroom/vrok/0.5.1/vrok":            MethodHomebrew,
		"/home/linuxbrew/.linuxbrew/Caskroom/vrok/1/vrok":   MethodHomebrew,
		`C:\Users\ana\scoop\apps\vrok\current\vrok.exe`:     MethodScoop,
		"/home/ana/gopath/bin/vrok":                         MethodGo,
		"/usr/local/bin/vrok":                               MethodSelf,
		"/home/ana/.local/bin/vrok":                         MethodSelf,
		`C:\Users\ana\AppData\Local\Programs\vrok\vrok.exe`: MethodSelf,
	}
	for path, want := range cases {
		if got := Detect(filepath.FromSlash(path)); got != want {
			t.Errorf("Detect(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestLatestReadsTheRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases/latest" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/releases/tag/v9.9.9", http.StatusFound)
	}))
	defer srv.Close()
	setBase(t, srv.URL+"/releases")

	tag, err := Latest(context.Background(), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v9.9.9" {
		t.Fatalf("tag = %q, want v9.9.9", tag)
	}
}

func TestApplyReplacesTheBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake release binary is a shell script")
	}
	archive := fakeArchive(t, "#!/bin/sh\necho 'vrok version 9.9.9 (test)'\n")
	srv := releaseServer(t, archive, sha(archive))
	setBase(t, srv.URL+"/releases")

	exe := filepath.Join(t.TempDir(), "vrok")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Apply(context.Background(), srv.Client(), "v9.9.9", exe); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	if !strings.Contains(string(got), "9.9.9") {
		t.Fatalf("binary was not replaced: %q", got)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".vrok-update-*"))
	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestApplyRefusesAMismatchedArchive(t *testing.T) {
	archive := fakeArchive(t, "#!/bin/sh\necho 'vrok version 9.9.9'\n")
	srv := releaseServer(t, archive, strings.Repeat("0", 64))
	setBase(t, srv.URL+"/releases")

	exe := filepath.Join(t.TempDir(), binaryName())
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := Apply(context.Background(), srv.Client(), "v9.9.9", exe)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatal("a mismatched archive replaced the binary")
	}
}

func TestApplyKeepsTheOldBinaryWhenTheNewOneDoesNotRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake release binary is a shell script")
	}
	// Runs, but is the wrong version: what a mislabelled asset would do.
	archive := fakeArchive(t, "#!/bin/sh\necho 'vrok version 0.0.1'\n")
	srv := releaseServer(t, archive, sha(archive))
	setBase(t, srv.URL+"/releases")

	exe := filepath.Join(t.TempDir(), "vrok")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Apply(context.Background(), srv.Client(), "v9.9.9", exe); err == nil {
		t.Fatal("a binary reporting the wrong version was installed")
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Fatal("the working binary was replaced by one that failed its check")
	}
}

func setBase(t *testing.T, base string) {
	t.Helper()
	old := releasesBase
	releasesBase = base
	t.Cleanup(func() { releasesBase = old })
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// releaseServer serves one release's archive and checksums.txt.
func releaseServer(t *testing.T, archive []byte, sum string) *httptest.Server {
	t.Helper()
	asset := archiveName("v9.9.9")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases/download/v9.9.9/checksums.txt":
			fmt.Fprintf(w, "%s  vrok_v9.9.9_plan9_mips.tar.gz\n%s  %s\n", strings.Repeat("f", 64), sum, asset)
		case "/releases/download/v9.9.9/" + asset:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeArchive builds a release-shaped tar.gz holding a "binary" plus the
// extras real archives carry.
func fakeArchive(t *testing.T, script string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct{ name, body string }{
		{"README.md", "readme"},
		{binaryName(), script},
		{"completions/vrok.bash", "complete"},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestTagNormalises(t *testing.T) {
	for in, want := range map[string]string{"0.6.0": "v0.6.0", "v0.6.0": "v0.6.0", " v1.2.3 ": "v1.2.3"} {
		got, err := Tag(in)
		if err != nil || got != want {
			t.Errorf("Tag(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"latest", "v1.2", "1.2.x", ""} {
		if _, err := Tag(bad); err == nil {
			t.Errorf("Tag(%q) accepted a non-release", bad)
		}
	}
}

func TestApplyNamesAMissingRelease(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	setBase(t, srv.URL+"/releases")

	err := Apply(context.Background(), srv.Client(), "v9.9.9", filepath.Join(t.TempDir(), "vrok"))
	if !errors.Is(err, ErrNoSuchRelease) {
		t.Fatalf("err = %v, want ErrNoSuchRelease", err)
	}
}
