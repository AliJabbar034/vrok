package inbox

import (
	"bytes"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) (*Inbox, string) {
	t.Helper()
	dir := t.TempDir()
	in, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close() })
	return in, dir
}

func send(t *testing.T, in *Inbox, name, body string) Received {
	t.Helper()
	id, err := in.Begin(name, int64(len(body)))
	if err != nil {
		t.Fatalf("Begin(%q): %v", name, err)
	}
	if _, err := in.Write(id, 0, strings.NewReader(body), nil); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := in.Finish(id)
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return got
}

func TestSafeNameKeepsUploadsInsideAndVisible(t *testing.T) {
	cases := map[string]string{
		"photo.jpg":             "photo.jpg",
		"../../etc/passwd":      "passwd",
		`..\..\Windows\win.ini`: "win.ini",
		"/abs/path/report.pdf":  "report.pdf",
		".bashrc":               "bashrc",
		"...":                   "file",
		"":                      "file",
		"a\x00b\x1fc.txt":       "abc.txt",
		`what?<now>.txt`:        "what__now_.txt",
		"CON.txt":               "_CON.txt",
		"nul":                   "_nul",
		"trailing. . ":          "trailing",
		"  spaced name.png  ":   "spaced name.png",
		"evil\u202egpj.exe":     "evilgpj.exe",
		"a\u009b31mb.txt":       "a31mb.txt",
		"\u2066left\u2069.pdf":  "left.pdf",
	}
	for in, want := range cases {
		if got := SafeName(in); got != want {
			t.Errorf("SafeName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 300) + ".mov"
	if got := SafeName(long); len(got) > maxNameBytes || !strings.HasSuffix(got, ".mov") || !strings.HasPrefix(got, "é") {
		t.Errorf("long name became %q (%d bytes)", got, len(got))
	}
}

func TestUploadLandsUnderItsName(t *testing.T) {
	in, dir := open(t)
	got := send(t, in, "notes.txt", "hello")
	if got.Name != "notes.txt" || got.Size != 5 {
		t.Fatalf("got %+v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("file holds %q, %v", data, err)
	}
	assertNoParts(t, dir)
}

// An upload must never replace a file the owner already has.
func TestExistingFilesAreNeverOverwritten(t *testing.T) {
	in, dir := open(t)
	if err := os.WriteFile(filepath.Join(dir, "photo.jpg"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := send(t, in, "photo.jpg", "new one")
	second := send(t, in, "photo.jpg", "newer one")
	if first.Name != "photo (1).jpg" || second.Name != "photo (2).jpg" {
		t.Fatalf("names = %q, %q", first.Name, second.Name)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "photo.jpg")); string(data) != "original" {
		t.Errorf("original was overwritten with %q", data)
	}
}

// A symlink in the inbox must not let a name write outside it.
func TestSymlinkInInboxCannotRedirectAWrite(t *testing.T) {
	in, dir := open(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "victim.txt")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "victim.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := send(t, in, "victim.txt", "attack")
	if got.Name == "victim.txt" {
		t.Fatal("upload took the symlink's name")
	}
	if data, _ := os.ReadFile(target); string(data) != "keep" {
		t.Errorf("file outside the inbox was changed to %q", data)
	}
}

func TestChunksResumeFromTheTruePosition(t *testing.T) {
	in, dir := open(t)
	body := "0123456789"
	id, err := in.Begin("data.bin", int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := in.Write(id, 0, strings.NewReader(body[:4]), nil); err != nil || n != 4 {
		t.Fatalf("first chunk: %d, %v", n, err)
	}
	// A retry of a chunk that already arrived is refused with the real
	// position, so the sender can carry on from there.
	if n, err := in.Write(id, 0, strings.NewReader(body[:4]), nil); !errors.Is(err, ErrOffset) || n != 4 {
		t.Fatalf("repeated chunk: %d, %v", n, err)
	}
	if written, size, err := in.Status(id); err != nil || written != 4 || size != 10 {
		t.Fatalf("Status = %d/%d, %v", written, size, err)
	}
	if _, err := in.Finish(id); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("early Finish: %v", err)
	}
	var seen int64
	if _, err := in.Write(id, 4, strings.NewReader(body[4:]), func(n int64) { seen += n }); err != nil {
		t.Fatal(err)
	}
	if seen != 6 {
		t.Errorf("progress saw %d bytes, want 6", seen)
	}
	if _, err := in.Finish(id); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "data.bin")); string(data) != body {
		t.Errorf("file holds %q", data)
	}
}

func TestMoreThanTheDeclaredSizeIsRefused(t *testing.T) {
	in, dir := open(t)
	id, err := in.Begin("small.txt", 3)
	if err != nil {
		t.Fatal(err)
	}
	n, err := in.Write(id, 0, bytes.NewReader([]byte("too long")), nil)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if n != 3 {
		t.Errorf("kept %d bytes, want the 3 declared", n)
	}
	in.Abort(id)
	assertNoParts(t, dir)
}

func TestUploadThatCannotFitIsRefusedUpFront(t *testing.T) {
	in, _ := open(t)
	in.freeSpace = func(string) int64 { return DiskReserve + 100 }
	if _, err := in.Begin("fits.bin", 100); err != nil {
		t.Fatalf("an upload that fits was refused: %v", err)
	}
	// The first upload's bytes are spoken for, even though none arrived.
	if _, err := in.Begin("second.bin", 1); !errors.Is(err, ErrNoSpace) {
		t.Fatalf("err = %v, want ErrNoSpace", err)
	}
}

func TestHugeDeclaredSizesCannotWrapPastTheDiskCheck(t *testing.T) {
	in, _ := open(t)
	in.freeSpace = func(string) int64 { return DiskReserve + 100 }
	if _, err := in.Begin("huge.bin", math.MaxInt64); !errors.Is(err, ErrNoSpace) {
		t.Errorf("Begin with the largest size: err = %v, want ErrNoSpace", err)
	}
	if err := in.Fits(math.MaxInt64); !errors.Is(err, ErrNoSpace) {
		t.Errorf("Fits with the largest size: err = %v, want ErrNoSpace", err)
	}
}

func TestCloseDiscardsUnfinishedUploads(t *testing.T) {
	dir := t.TempDir()
	in, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	send(t, in, "done.txt", "kept")
	id, err := in.Begin("half.txt", 10)
	if err != nil {
		t.Fatal(err)
	}
	in.Write(id, 0, strings.NewReader("12345"), nil)
	in.Close()

	assertNoParts(t, dir)
	if _, err := os.Stat(filepath.Join(dir, "done.txt")); err != nil {
		t.Errorf("a finished file was removed: %v", err)
	}
}

func assertNoParts(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), partPrefix) {
			t.Errorf("part file left behind: %s", e.Name())
		}
	}
}
