package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AliJabbar034/vrok/internal/security"
	"github.com/AliJabbar034/vrok/internal/server"
	"github.com/AliJabbar034/vrok/internal/sharing"
)

// A Range header is whatever the client chose to send, so no spelling of it
// may turn a new download into a free one. Each request here comes from a
// different visitor with no session; only the first may receive the file.
func TestRangeTricksCannotBypassTheDownloadLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.bin")
	content := strings.Repeat("x", 1000)
	write(t, path, content)

	f := newFixture(t, sharing.Spec{
		Kind: sharing.KindFile, Name: "secret.bin",
		Entries:      []sharing.Entry{{Name: "secret.bin", Path: path}},
		MaxDownloads: 1,
	})

	served := 0
	for _, spec := range []string{"bytes=-999999", "bytes=1-", "bytes=0-", "bytes=5-9,0-4", ""} {
		req, _ := http.NewRequest(http.MethodGet, f.url("?dl=1"), nil)
		if spec != "" {
			req.Header.Set("Range", spec)
		}
		resp, err := http.DefaultClient.Do(req) // no jar: a fresh visitor each time
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
			served++
		}
	}
	if served != 1 {
		t.Errorf("%d visitors received file content under --downloads 1, want 1", served)
	}
	if got := f.share.Snapshot().Downloads; got != 1 {
		t.Errorf("counted %d downloads, want 1", got)
	}
}

// The visitor who used the last download must still be able to seek through
// the video or resume the transfer; nobody else may start one, and browsing
// stops for everyone.
func TestTheLastDownloadCanStillSeek(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mp4")
	write(t, path, strings.Repeat("v", 1000))

	f := newFixture(t, sharing.Spec{
		Kind: sharing.KindFile, Name: "clip.mp4",
		Entries:      []sharing.Entry{{Name: "clip.mp4", Path: path}},
		MaxDownloads: 1,
	})

	for i, spec := range []string{"bytes=0-99", "bytes=500-599", "bytes=-50", "bytes=100-"} {
		if resp := f.get("?raw=1", [2]string{"Range", spec}); resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("seek %d (%s) by the paying visitor returned %d, want 206", i, spec, resp.StatusCode)
		}
	}
	if got := f.share.Snapshot().Downloads; got != 1 {
		t.Errorf("one visitor seeking was counted as %d downloads, want 1", got)
	}
	if resp := f.get(""); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the preview page returned %d after the limit was reached, want 404", resp.StatusCode)
	}
	other, err := http.Get(f.url("?raw=1"))
	if err != nil {
		t.Fatal(err)
	}
	other.Body.Close()
	if other.StatusCode != http.StatusNotFound {
		t.Errorf("a second visitor got %d, want 404", other.StatusCode)
	}
}

// A session for one file must not make other files in the same share free.
func TestADownloadSessionCoversOnlyItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.bin"), "aaaa")
	write(t, filepath.Join(dir, "b.bin"), "bbbb")

	f := newFixture(t, sharing.Spec{Kind: sharing.KindDirectory, Name: "dir", Root: dir})

	f.get("a.bin?dl=1")
	f.get("a.bin?dl=1", [2]string{"Range", "bytes=1-"})
	f.get("b.bin?dl=1", [2]string{"Range", "bytes=1-"})

	if got := f.share.Snapshot().Downloads; got != 2 {
		t.Errorf("counted %d downloads, want 2: one for a.bin, and b.bin is a new file", got)
	}
}

// A 304 delivers nothing, so it neither spends the allowance nor earns a
// session that would make later ranged requests free.
func TestNotModifiedEarnsNoDownloadSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	write(t, path, "data")

	f := newFixture(t, sharing.Spec{
		Kind: sharing.KindFile, Name: "file.txt",
		Entries:      []sharing.Entry{{Name: "file.txt", Path: path}},
		MaxDownloads: 1,
	})

	head, err := http.Head(f.url("?dl=1"))
	if err != nil {
		t.Fatal(err)
	}
	head.Body.Close()

	resp := f.get("?dl=1", [2]string{"If-None-Match", head.Header.Get("ETag")})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional GET returned %d, want 304", resp.StatusCode)
	}
	if cookies := resp.Header.Values("Set-Cookie"); len(cookies) > 0 {
		t.Errorf("a 304 issued %v", cookies)
	}
	if got := f.share.Snapshot().Downloads; got != 0 {
		t.Errorf("a 304 was counted as %d downloads", got)
	}
}

// The preview frames HTML in a sandbox, but the raw URL can be opened
// directly. It must be sandboxed there too, for every type a browser would
// run script from. Directory shares are exempt by design.
func TestRawActiveContentIsSandboxed(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"page.html", "image.svg", "feed.xml", "notes.txt", "pic.png"} {
		write(t, filepath.Join(dir, name), "<script>alert(1)</script>")
	}
	active := map[string]bool{"page.html": true, "image.svg": true, "feed.xml": true}

	for name, isActive := range map[string]bool{"page.html": true, "image.svg": true, "feed.xml": true, "notes.txt": false, "pic.png": false} {
		f := newFixture(t, sharing.Spec{
			Kind: sharing.KindFile, Name: name,
			Entries: []sharing.Entry{{Name: name, Path: filepath.Join(dir, name)}},
		})
		got := f.get("?raw=1").Header.Get("Content-Security-Policy")
		if isActive && got != "sandbox" {
			t.Errorf("%s served raw with CSP %q, want sandbox", name, got)
		}
		if !isActive && got != "" {
			t.Errorf("%s served raw with CSP %q, want none", name, got)
		}
	}

	f := newFixture(t, sharing.Spec{Kind: sharing.KindDirectory, Name: "site", Root: dir})
	for name := range active {
		if got := f.get(name + "?raw=1").Header.Get("Content-Security-Policy"); got != "" {
			t.Errorf("directory share sandboxed %s (%q); built sites need their scripts", name, got)
		}
	}
}

// A dev server behind a password must keep working after the visitor
// unlocks it, including the absolute asset paths routed by Referer. The app
// itself must never see vrok's unlock cookie.
func TestProtectedHTTPShareServesAbsoluteAssetsAfterUnlock(t *testing.T) {
	var sawCookie atomic.Value
	sawCookie.Store("")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawCookie.Store(r.Header.Get("Cookie"))
		io.WriteString(w, "upstream "+r.URL.Path)
	}))
	defer upstream.Close()

	f := newFixture(t, sharing.Spec{
		Kind: sharing.KindHTTP, Name: "app", Target: upstream.URL,
		PasswordHash: "h:pw",
	})
	if resp := f.post(t, "", url.Values{"password": {"pw"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("unlock returned %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/assets/app.js", nil)
	req.Header.Set("Referer", f.url(""))
	req.AddCookie(&http.Cookie{Name: "app_session", Value: "keep-me"})
	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := f.body(resp)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK || body != "upstream /assets/app.js" {
		t.Fatalf("stray asset after unlock = %d %q, want the upstream asset", resp.StatusCode, body)
	}
	cookie := sawCookie.Load().(string)
	if strings.Contains(cookie, "vrok_") {
		t.Errorf("upstream received vrok's cookie: %q", cookie)
	}
	if !strings.Contains(cookie, "app_session=keep-me") {
		t.Errorf("upstream lost the app's own cookie: %q", cookie)
	}

	// Without the unlock, the same stray request must still be refused.
	anon, _ := http.NewRequest(http.MethodGet, f.server.URL+"/assets/app.js", nil)
	anon.Header.Set("Referer", f.url(""))
	locked, err := http.DefaultClient.Do(anon)
	if err != nil {
		t.Fatal(err)
	}
	locked.Body.Close()
	if locked.StatusCode != http.StatusUnauthorized {
		t.Errorf("stray asset without unlock returned %d, want 401", locked.StatusCode)
	}
}

// slowHasher measures how many verifications run at once.
type slowHasher struct {
	running, peak atomic.Int32
}

func (h *slowHasher) Hash(p string) (string, error) { return "h:" + p, nil }

func (h *slowHasher) Verify(p, encoded string) error {
	n := h.running.Add(1)
	defer h.running.Add(-1)
	for {
		peak := h.peak.Load()
		if n <= peak || h.peak.CompareAndSwap(peak, n) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	if "h:"+p != encoded {
		return security.ErrPasswordMismatch
	}
	return nil
}

// Each real verification allocates 64 MiB, so a burst of guesses must queue
// rather than run all at once.
func TestPasswordVerificationsAreBounded(t *testing.T) {
	hasher := &slowHasher{}
	share := sharing.New(sharing.Spec{
		ID: "testid", Token: "test-token", Kind: sharing.KindFile, Name: "f",
		Entries:      []sharing.Entry{{Name: "f", Path: filepath.Join(t.TempDir(), "f")}},
		PasswordHash: "h:right", CreatedAt: time.Now(),
	})
	registry := sharing.NewRegistry()
	registry.Add(share)
	srv, err := server.New(server.Options{Resolver: registry, Hasher: hasher})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.PostForm(ts.URL+server.SharePath("test-token"), url.Values{"password": {"guess"}})
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()

	if peak := hasher.peak.Load(); peak > 2 {
		t.Errorf("%d password verifications ran at once, want at most 2", peak)
	}
}
