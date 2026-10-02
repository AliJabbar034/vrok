package server_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AliJabbar034/vrok/internal/security"
	"github.com/AliJabbar034/vrok/internal/server"
	"github.com/AliJabbar034/vrok/internal/sharing"
)

// testHasher keeps Argon2's cost out of the test suite while exercising the
// same interface the real hasher implements.
type testHasher struct{}

func (testHasher) Hash(password string) (string, error) { return "h:" + password, nil }

func (testHasher) Verify(password, encoded string) error {
	if "h:"+password != encoded {
		return security.ErrPasswordMismatch
	}
	return nil
}

// fixture is a running server with one share.
type fixture struct {
	t      *testing.T
	server *httptest.Server
	share  *sharing.Share
	client *http.Client
}

func newFixture(t *testing.T, spec sharing.Spec) *fixture {
	t.Helper()

	if spec.ID == "" {
		spec.ID = "testid"
	}
	if spec.Token == "" {
		spec.Token = "test-token"
	}
	if spec.CreatedAt.IsZero() {
		spec.CreatedAt = time.Now()
	}

	share := sharing.New(spec)
	registry := sharing.NewRegistry()
	if err := registry.Add(share); err != nil {
		t.Fatalf("register share: %v", err)
	}

	key, err := security.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(server.Options{
		Resolver: registry,
		Hasher:   testHasher{},
		Signer:   security.NewHMACSigner(key),
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	jar := newJar()
	return &fixture{
		t:      t,
		server: ts,
		share:  share,
		client: &http.Client{
			Jar: jar,
			// Redirects are part of what is being tested, so they are never
			// followed implicitly.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// url builds an absolute URL for a path inside the share.
func (f *fixture) url(rel string) string {
	return f.server.URL + server.SharePath(f.share.Token()) + strings.TrimPrefix(rel, "/")
}

func (f *fixture) get(rel string, headers ...[2]string) *http.Response {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.url(rel), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("GET %s: %v", rel, err)
	}
	f.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (f *fixture) body(resp *http.Response) string {
	f.t.Helper()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

func TestSingleFileSharePreviewAndDownload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	content := "hello vrok\n"
	write(t, path, content)

	f := newFixture(t, sharing.Spec{
		Kind:    sharing.KindFile,
		Name:    "notes.txt",
		Entries: []sharing.Entry{{Name: "notes.txt", Path: path, Size: int64(len(content))}},
	})

	t.Run("root renders a preview page", func(t *testing.T) {
		resp := f.get("")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		body := f.body(resp)
		if !strings.Contains(body, "notes.txt") || !strings.Contains(body, "hello vrok") {
			t.Error("the preview page does not show the file name and contents")
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			t.Errorf("Content-Type = %q, want HTML", resp.Header.Get("Content-Type"))
		}
	})

	t.Run("raw serves the bytes", func(t *testing.T) {
		resp := f.get("?raw=1")
		if got := f.body(resp); got != content {
			t.Errorf("raw body = %q, want %q", got, content)
		}
		if resp.Header.Get("Accept-Ranges") != "bytes" {
			t.Error("the raw route does not advertise range support")
		}
	})

	t.Run("download forces an attachment", func(t *testing.T) {
		resp := f.get("?dl=1")
		if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
			t.Errorf("Content-Disposition = %q, want an attachment", cd)
		}
	})

	t.Run("the file also answers under its own name", func(t *testing.T) {
		if resp := f.get("notes.txt"); resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("any other path is not found", func(t *testing.T) {
		if resp := f.get("other.txt"); resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("a sibling file is not reachable", func(t *testing.T) {
		write(t, filepath.Join(dir, "secret.txt"), "classified")
		resp := f.get("secret.txt")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
		if body := f.body(resp); strings.Contains(body, "classified") {
			t.Error("a sibling file leaked through a single-file share")
		}
	})

	t.Run("writes are refused", func(t *testing.T) {
		resp, err := f.client.Post(f.url(""), "text/plain", strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST to an unprotected file share returned %d, want 405", resp.StatusCode)
		}
	})
}

func TestSingleFileShareRejectsASymlinkSwap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	write(t, path, "hello vrok\n")
	secret := filepath.Join(t.TempDir(), "secret.txt")
	write(t, secret, "classified")

	f := newFixture(t, sharing.Spec{
		Kind:    sharing.KindFile,
		Name:    "notes.txt",
		Entries: []sharing.Entry{{Name: "notes.txt", Path: path}},
	})

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, path); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}

	resp := f.get("?raw=1")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 after the file was swapped for a symlink", resp.StatusCode)
	}
	if body := f.body(resp); strings.Contains(body, "classified") {
		t.Error("a symlink swap leaked a file that was never shared")
	}
}

func TestRangeRequestsAndConditionalGets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "video.mp4")
	// Content that makes an offset visible in the response.
	content := strings.Repeat("0123456789", 100)
	write(t, path, content)

	f := newFixture(t, sharing.Spec{
		Kind:    sharing.KindFile,
		Name:    "video.mp4",
		Entries: []sharing.Entry{{Name: "video.mp4", Path: path}},
	})

	t.Run("partial content", func(t *testing.T) {
		resp := f.get("?raw=1", [2]string{"Range", "bytes=10-19"})
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", resp.StatusCode)
		}
		if got := f.body(resp); got != "0123456789" {
			t.Errorf("body = %q, want the bytes at offset 10", got)
		}
		if want := fmt.Sprintf("bytes 10-19/%d", len(content)); resp.Header.Get("Content-Range") != want {
			t.Errorf("Content-Range = %q, want %q", resp.Header.Get("Content-Range"), want)
		}
	})

	t.Run("suffix range", func(t *testing.T) {
		resp := f.get("?raw=1", [2]string{"Range", "bytes=-5"})
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", resp.StatusCode)
		}
		if got := f.body(resp); got != "56789" {
			t.Errorf("body = %q, want the last five bytes", got)
		}
	})

	t.Run("unsatisfiable range", func(t *testing.T) {
		resp := f.get("?raw=1", [2]string{"Range", "bytes=999999-"})
		if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
			t.Errorf("status = %d, want 416", resp.StatusCode)
		}
	})

	t.Run("etag enables a 304 and a resumable range", func(t *testing.T) {
		first := f.get("?raw=1")
		etag := first.Header.Get("ETag")
		if etag == "" {
			t.Fatal("no ETag was sent")
		}
		if strings.HasPrefix(etag, "W/") {
			t.Error("the ETag is weak, so If-Range cannot be used to resume a download")
		}

		notModified := f.get("?raw=1", [2]string{"If-None-Match", etag})
		if notModified.StatusCode != http.StatusNotModified {
			t.Errorf("conditional GET returned %d, want 304", notModified.StatusCode)
		}

		resumed := f.get("?raw=1",
			[2]string{"If-Range", etag},
			[2]string{"Range", "bytes=995-"})
		if resumed.StatusCode != http.StatusPartialContent {
			t.Errorf("If-Range resume returned %d, want 206", resumed.StatusCode)
		}
	})

	t.Run("HEAD reports metadata without a body", func(t *testing.T) {
		resp, err := f.client.Head(f.url("?raw=1"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.ContentLength != int64(len(content)) {
			t.Errorf("Content-Length = %d, want %d", resp.ContentLength, len(content))
		}
		if body, _ := io.ReadAll(resp.Body); len(body) != 0 {
			t.Errorf("HEAD returned %d bytes of body", len(body))
		}
	})
}

// Every path a share cannot serve must answer identically. A 500 for an
// escaping symlink and a 404 for a typo would tell a prober which paths exist
// outside the share, which is the disclosure the confinement exists to
// prevent.
func TestUnservablePathsAreIndistinguishable(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "real.txt"), "content")
	write(t, filepath.Join(filepath.Dir(root), "secret.txt"), "classified")

	// A symlink that escapes the root, and one that points at itself. Both
	// previously produced a 500 that a missing file did not.
	if err := os.Symlink(filepath.Join(filepath.Dir(root), "secret.txt"),
		filepath.Join(root, "escape")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}

	// A file that exists in the listing but is gone by the time it is fetched.
	vanishing := filepath.Join(root, "vanishing.txt")
	write(t, vanishing, "temporary")

	f := newFixture(t, sharing.Spec{
		Kind: sharing.KindDirectory,
		Name: filepath.Base(root),
		Root: root,
	})

	if err := os.Remove(vanishing); err != nil {
		t.Fatal(err)
	}

	// The baseline: a path that simply is not there.
	want := f.body(f.get("nosuchfile.txt?raw=1"))

	for _, rel := range []string{
		"escape",
		"loop",
		"vanishing.txt",
		"deeply/nested/nothing",
	} {
		t.Run(rel, func(t *testing.T) {
			resp := f.get(rel + "?raw=1")
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("status = %d, want 404", resp.StatusCode)
			}
			if got := f.body(resp); got != want {
				t.Errorf("the response differs from a plain missing file, which is a probe signal")
			}
		})
	}

	// The confinement must not have broken ordinary access.
	if got := f.body(f.get("real.txt?raw=1")); got != "content" {
		t.Errorf("real.txt body = %q, want %q", got, "content")
	}
}

// A listing is rendered into memory before it is written, so an unbounded one
// would let `vrok ./node_modules` allocate tens of megabytes per request.
// Capping it must not make any file unreachable.
func TestHugeDirectoryListingsAreBounded(t *testing.T) {
	root := t.TempDir()
	const count = 2500
	for i := range count {
		write(t, filepath.Join(root, fmt.Sprintf("file%04d.txt", i)), "x")
	}

	f := newFixture(t, sharing.Spec{
		Kind: sharing.KindDirectory,
		Name: filepath.Base(root),
		Root: root,
	})

	body := f.body(f.get("?list=1"))

	// The cap is applied after sorting, so the page shows the alphabetical
	// start of the directory rather than an arbitrary slice of readdir order.
	if !strings.Contains(body, "file0000.txt") {
		t.Error("the listing does not start at the first entry alphabetically")
	}
	if strings.Contains(body, "file2499.txt") {
		t.Error("the listing was not capped")
	}
	if !strings.Contains(body, "2500 entries") {
		t.Error("the page does not say how many entries were hidden")
	}

	// Being left off the page must not make a file unreachable: the cap is a
	// rendering limit, not an access rule.
	if resp := f.get("file2499.txt?raw=1"); resp.StatusCode != http.StatusOK {
		t.Errorf("a file past the listing cap returned %d, want 200", resp.StatusCode)
	}
}

func TestDirectoryShareIsConfinedToItsRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Dir(root)
	write(t, filepath.Join(outside, "secret.txt"), "classified")

	write(t, filepath.Join(root, "index.html"), "<h1>report</h1>")
	if err := os.MkdirAll(filepath.Join(root, "screenshots"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "screenshots", "shot.png"), "png-bytes")

	f := newFixture(t, sharing.Spec{Kind: sharing.KindDirectory, Name: filepath.Base(root), Root: root})

	t.Run("serves index.html at the root", func(t *testing.T) {
		resp := f.get("")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if got := f.body(resp); got != "<h1>report</h1>" {
			t.Errorf("body = %q, want the index document itself", got)
		}
	})

	t.Run("list=1 shows the directory contents", func(t *testing.T) {
		body := f.body(f.get("?list=1"))
		for _, want := range []string{"index.html", "screenshots"} {
			if !strings.Contains(body, want) {
				t.Errorf("the listing does not mention %q", want)
			}
		}
	})

	t.Run("serves nested files", func(t *testing.T) {
		if got := f.body(f.get("screenshots/shot.png?raw=1")); got != "png-bytes" {
			t.Errorf("nested file body = %q", got)
		}
	})

	t.Run("refuses every traversal attempt", func(t *testing.T) {
		// Each of these is a different encoding of the same attack, and each
		// must be indistinguishable from a missing file.
		attacks := []string{
			"../secret.txt",
			"..%2Fsecret.txt",
			"%2e%2e/secret.txt",
			"screenshots/../../secret.txt",
			"....//secret.txt",
			"/etc/passwd",
		}
		for _, attack := range attacks {
			resp := f.get(attack)
			if resp.StatusCode == http.StatusOK {
				t.Errorf("%s was served with status 200", attack)
			}
			if body := f.body(resp); strings.Contains(body, "classified") {
				t.Errorf("%s leaked the contents of a file outside the share", attack)
			}
		}
	})

	t.Run("a directory without a slash redirects", func(t *testing.T) {
		resp := f.get("screenshots")
		if resp.StatusCode != http.StatusMovedPermanently {
			t.Fatalf("status = %d, want 301", resp.StatusCode)
		}
		if location := resp.Header.Get("Location"); !strings.HasSuffix(location, "/screenshots/") {
			t.Errorf("Location = %q, want a trailing slash", location)
		}
	})
}

func TestPasswordGate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.pdf")
	write(t, path, "pdf-bytes")

	f := newFixture(t, sharing.Spec{
		Kind:         sharing.KindFile,
		Name:         "secret.pdf",
		Entries:      []sharing.Entry{{Name: "secret.pdf", Path: path}},
		PasswordHash: "h:open-sesame",
	})

	t.Run("content is withheld until unlocked", func(t *testing.T) {
		for _, rel := range []string{"", "?raw=1", "?dl=1"} {
			resp := f.get(rel)
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("GET %q returned %d, want 401", rel, resp.StatusCode)
			}
			if body := f.body(resp); strings.Contains(body, "pdf-bytes") {
				t.Errorf("GET %q served the file without a password", rel)
			}
		}
	})

	t.Run("the gate page does not leak the filename", func(t *testing.T) {
		if body := f.body(f.get("")); strings.Contains(body, "secret.pdf") {
			t.Error("the password page reveals the name of the protected file")
		}
	})

	t.Run("a wrong password is refused", func(t *testing.T) {
		resp := f.post(t, "", url.Values{"password": {"guess"}})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
		if !strings.Contains(f.body(resp), "Incorrect password") {
			t.Error("no error message was shown")
		}
	})

	t.Run("the right password unlocks the share", func(t *testing.T) {
		resp := f.post(t, "", url.Values{"password": {"open-sesame"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303", resp.StatusCode)
		}
		if len(resp.Cookies()) == 0 {
			t.Fatal("no unlock cookie was set")
		}
		cookie := resp.Cookies()[0]
		if !cookie.HttpOnly {
			t.Error("the unlock cookie is readable by scripts")
		}
		if strings.Contains(cookie.Value, "open-sesame") {
			t.Error("the unlock cookie contains the password")
		}

		// The client's jar now holds the cookie, so content is available.
		if got := f.body(f.get("?raw=1")); got != "pdf-bytes" {
			t.Errorf("body after unlock = %q, want the file", got)
		}
	})
}

func TestUnavailableSharesReportTheSameWay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	write(t, path, "data")

	entries := []sharing.Entry{{Name: "file.txt", Path: path}}

	t.Run("expired", func(t *testing.T) {
		f := newFixture(t, sharing.Spec{
			Kind: sharing.KindFile, Name: "file.txt", Entries: entries,
			ExpiresAt: time.Now().Add(-time.Minute),
		})
		resp := f.get("")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
		if !strings.Contains(f.body(resp), "expired") {
			t.Error("the page does not say the share expired")
		}
	})

	t.Run("download limit reached", func(t *testing.T) {
		f := newFixture(t, sharing.Spec{
			Kind: sharing.KindFile, Name: "file.txt", Entries: entries,
			MaxDownloads: 1,
		})

		if resp := f.get("?dl=1"); resp.StatusCode != http.StatusOK {
			t.Fatalf("the first download returned %d", resp.StatusCode)
		}
		// A second visitor, with no session for the first download.
		other, err := http.Get(f.url("?dl=1"))
		if err != nil {
			t.Fatal(err)
		}
		other.Body.Close()
		if other.StatusCode != http.StatusNotFound {
			t.Errorf("the second download returned %d, want 404", other.StatusCode)
		}
		// Browsing must stop too, not just downloading.
		if resp := f.get(""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("the page returned %d after the limit was reached, want 404", resp.StatusCode)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		f := newFixture(t, sharing.Spec{Kind: sharing.KindFile, Name: "file.txt", Entries: entries})
		f.share.Revoke()
		if resp := f.get(""); resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
	})

	t.Run("unknown token", func(t *testing.T) {
		f := newFixture(t, sharing.Spec{Kind: sharing.KindFile, Name: "file.txt", Entries: entries})
		resp, err := f.client.Get(f.server.URL + "/s/some-other-token/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
	})
}

func TestRangedMediaRequestsDoNotExhaustTheDownloadLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.mp4")
	write(t, path, strings.Repeat("x", 1000))

	f := newFixture(t, sharing.Spec{
		Kind:         sharing.KindFile,
		Name:         "clip.mp4",
		Entries:      []sharing.Entry{{Name: "clip.mp4", Path: path}},
		MaxDownloads: 2,
	})

	// A browser seeking through a video issues many ranged requests. Only the
	// one starting at byte zero counts, otherwise --downloads would be spent
	// before the video finished loading.
	for i, spec := range []string{"bytes=0-99", "bytes=100-199", "bytes=500-599", "bytes=-50"} {
		resp := f.get("?raw=1", [2]string{"Range", spec})
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("request %d (%s) returned %d", i, spec, resp.StatusCode)
		}
	}
	// HEAD must not count either.
	head, err := f.client.Head(f.url("?raw=1"))
	if err != nil {
		t.Fatal(err)
	}
	head.Body.Close()

	if got := f.share.Snapshot().Downloads; got != 1 {
		t.Errorf("counted %d downloads across four ranged requests and a HEAD, want 1", got)
	}
}

func TestMultiFileShareIndex(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "report.pdf"), "pdf")
	write(t, filepath.Join(dir, "shot.png"), "png")

	f := newFixture(t, sharing.Spec{
		Kind: sharing.KindFiles,
		Name: "2 files",
		Entries: []sharing.Entry{
			{Name: "report.pdf", Path: filepath.Join(dir, "report.pdf")},
			{Name: "shot.png", Path: filepath.Join(dir, "shot.png")},
		},
	})

	body := f.body(f.get(""))
	for _, want := range []string{"report.pdf", "shot.png", "Shared files"} {
		if !strings.Contains(body, want) {
			t.Errorf("the index does not mention %q", want)
		}
	}

	if got := f.body(f.get("report.pdf?raw=1")); got != "pdf" {
		t.Errorf("file body = %q", got)
	}
	// Only the generated names are addressable, never a path.
	if resp := f.get("../" + filepath.Base(dir) + "/report.pdf"); resp.StatusCode == http.StatusOK {
		t.Error("a path-shaped request reached a file in a multi-file share")
	}
}

func TestStaticAssetsAreServed(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "a")
	f := newFixture(t, sharing.Spec{
		Kind:    sharing.KindFile,
		Name:    "a.txt",
		Entries: []sharing.Entry{{Name: "a.txt", Path: filepath.Join(dir, "a.txt")}},
	})

	resp, err := f.client.Get(f.server.URL + "/_vrok/static/viewer.css")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/css") {
		t.Errorf("Content-Type = %q, want CSS", resp.Header.Get("Content-Type"))
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
