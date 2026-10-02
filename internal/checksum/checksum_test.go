package checksum

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func waitFor(t *testing.T, c *Cache, path string) (string, State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		sum, state := c.Lookup(path, "", info)
		if state != Pending || time.Now().After(deadline) {
			return sum, state
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLookupComputesInTheBackground(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("hello vrok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := New()

	sum, state := waitFor(t, c, path)
	if state != Ready {
		t.Fatalf("state = %v, want Ready", state)
	}
	// printf 'hello vrok\n' | shasum -a 256
	const want = "f2f84dd85dc86116aa6a06d655bf6c02f1543da9242884813c709c81a0417287"
	if sum != want {
		t.Fatalf("sum = %q, want %q", sum, want)
	}
}

func TestEditingAFileGivesANewFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := New()
	first, _ := waitFor(t, c, path)

	if err := os.WriteFile(path, []byte("two!"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, state := waitFor(t, c, path)
	if state != Ready || second == first {
		t.Fatalf("after an edit got %q (%v), want a new fingerprint", second, state)
	}
}

func TestUnreadableFileIsUnavailable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	c := New()
	// A single-file share whose file was swapped for a symlink must not be
	// read, for hashing any more than for downloading.
	if _, state := waitFor(t, c, link); state != Unavailable {
		t.Fatalf("state = %v, want Unavailable", state)
	}
}
