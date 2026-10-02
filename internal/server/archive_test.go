package server_test

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/AliJabbar034/vrok/internal/sharing"
)

func readZip(t *testing.T, body []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("response is not a valid zip: %v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(data)
	}
	return files
}

func names(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestDirectoryDownloadsAsOneZip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "report")
	if err := os.MkdirAll(filepath.Join(root, "sub", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "a.txt"), "alpha")
	write(t, filepath.Join(root, "sub", "b.mp4"), "beta")
	outside := filepath.Join(t.TempDir(), "secret.txt")
	write(t, outside, "secret")
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}

	f := newFixture(t, sharing.Spec{Kind: sharing.KindDirectory, Name: "report", Root: root})

	t.Run("listing offers the zip", func(t *testing.T) {
		body := f.body(f.get(""))
		if !strings.Contains(body, "?zip=1") {
			t.Fatalf("listing has no Download all link:\n%s", body)
		}
	})

	t.Run("whole share", func(t *testing.T) {
		resp := f.get("?zip=1")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if got := resp.Header.Get("Content-Type"); got != "application/zip" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := resp.Header.Get("Content-Disposition"); !strings.Contains(got, `filename=report.zip`) {
			t.Errorf("Content-Disposition = %q", got)
		}
		data, _ := io.ReadAll(resp.Body)
		files := readZip(t, data)
		if files["report/a.txt"] != "alpha" || files["report/sub/b.mp4"] != "beta" {
			t.Fatalf("zip contents = %v", names(files))
		}
		if _, ok := files["report/sub/empty/"]; !ok {
			t.Errorf("empty folder missing from zip: %v", names(files))
		}
		// A symlink pointing outside the share must not smuggle the target
		// into the archive.
		if _, ok := files["report/escape.txt"]; ok {
			t.Fatal("zip included a symlink that escapes the share")
		}
	})

	t.Run("subfolder", func(t *testing.T) {
		data, _ := io.ReadAll(f.get("sub?zip=1").Body)
		files := readZip(t, data)
		if len(files) != 3 || files["sub/b.mp4"] != "beta" {
			t.Fatalf("subfolder zip = %v", names(files))
		}
	})
}

func TestZipCountsAsOneDownload(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.txt"), "alpha")
	write(t, filepath.Join(root, "b.txt"), "beta")
	f := newFixture(t, sharing.Spec{Kind: sharing.KindDirectory, Name: "d", Root: root, MaxDownloads: 1})

	if resp := f.get("?zip=1"); resp.StatusCode != http.StatusOK {
		t.Fatalf("first zip: status %d", resp.StatusCode)
	}
	if got := f.share.Snapshot().Downloads; got != 1 {
		t.Fatalf("downloads = %d, want 1", got)
	}
	if resp := f.get("?zip=1"); resp.StatusCode == http.StatusOK {
		t.Fatal("a second zip was allowed past --downloads 1")
	}
	if got := f.share.Snapshot().BytesTransferred; got == 0 {
		t.Error("zip bytes were not counted")
	}
}

func TestFileSetDownloadsAsOneZip(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	write(t, a, "alpha")
	write(t, b, "beta")
	f := newFixture(t, sharing.Spec{
		Kind: sharing.KindFiles,
		Name: "2 files",
		Entries: []sharing.Entry{
			{Name: "a.txt", Path: a, Size: 5},
			{Name: "b.txt", Path: b, Size: 4},
		},
	})

	data, _ := io.ReadAll(f.get("?zip=1").Body)
	files := readZip(t, data)
	if files["a.txt"] != "alpha" || files["b.txt"] != "beta" || len(files) != 2 {
		t.Fatalf("zip = %v", names(files))
	}
}

func TestFilePageShowsSHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	write(t, path, "hello vrok\n")
	f := newFixture(t, sharing.Spec{
		Kind:    sharing.KindFile,
		Name:    "notes.txt",
		Entries: []sharing.Entry{{Name: "notes.txt", Path: path, Size: 11}},
	})

	// printf 'hello vrok\n' | shasum -a 256
	const want = "f2f84dd85dc86116aa6a06d655bf6c02f1543da9242884813c709c81a0417287"
	deadline := time.Now().Add(5 * time.Second)
	for {
		body := f.body(f.get(""))
		if strings.Contains(body, want) {
			return
		}
		if !strings.Contains(body, "calculating") {
			t.Fatalf("page shows neither the checksum nor that it is pending:\n%s", body)
		}
		if time.Now().After(deadline) {
			t.Fatal("checksum never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
