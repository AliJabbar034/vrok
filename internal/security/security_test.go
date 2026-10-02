package security_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AliJabbar034/vrok/internal/security"
)

func TestCleanRelativeRejectsEscapes(t *testing.T) {
	// These are the inputs that matter: every one of them is a documented way
	// of climbing out of a served directory.
	escapes := []string{
		"..",
		"../",
		"../etc/passwd",
		"../../../../etc/passwd",
		"a/../../b",
		"/../etc/passwd",
		"..\\..\\windows\\system32",
		"a/b/../../../c",
		"foo\x00.txt",
	}
	for _, input := range escapes {
		t.Run(input, func(t *testing.T) {
			if got, err := security.CleanRelative(input); err == nil {
				t.Fatalf("CleanRelative(%q) = %q, want an error", input, got)
			}
		})
	}
}

func TestCleanRelativeAcceptsSafePaths(t *testing.T) {
	cases := map[string]string{
		"":                ".",
		"/":               ".",
		"index.html":      "index.html",
		"/index.html":     "index.html",
		"a/b/c.txt":       filepath.Join("a", "b", "c.txt"),
		"./a/./b.txt":     filepath.Join("a", "b.txt"),
		"a/b/../c.txt":    filepath.Join("a", "c.txt"),
		"weird name.txt":  "weird name.txt",
		"dots../file.txt": filepath.Join("dots..", "file.txt"),
	}
	for input, want := range cases {
		got, err := security.CleanRelative(input)
		if err != nil {
			t.Fatalf("CleanRelative(%q) returned %v", input, err)
		}
		if got != want {
			t.Errorf("CleanRelative(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPathResolverConfinesToRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	writeFile(t, filepath.Join(root, "inside.txt"), "ok")
	writeFile(t, filepath.Join(outside, "secret.txt"), "classified")
	mkdir(t, filepath.Join(root, "sub"))
	writeFile(t, filepath.Join(root, "sub", "nested.txt"), "ok")

	resolver, err := security.NewPathResolver(root)
	if err != nil {
		t.Fatalf("NewPathResolver: %v", err)
	}

	t.Run("resolves files inside the root", func(t *testing.T) {
		for _, rel := range []string{"inside.txt", "sub/nested.txt", "sub/../inside.txt"} {
			got, err := resolver.Resolve(rel)
			if err != nil {
				t.Fatalf("Resolve(%q) returned %v", rel, err)
			}
			if !strings.HasPrefix(got, resolver.Root()) {
				t.Errorf("Resolve(%q) = %q, which is outside %q", rel, got, resolver.Root())
			}
		}
	})

	t.Run("rejects traversal", func(t *testing.T) {
		for _, rel := range []string{"../", "../secret.txt", "sub/../../secret.txt"} {
			if _, err := resolver.Resolve(rel); err == nil {
				t.Errorf("Resolve(%q) succeeded, want refusal", rel)
			}
		}
	})

	t.Run("rejects symlinks escaping the root", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation needs privileges on Windows")
		}
		// This is the check a lexical path cleaner cannot make: the path never
		// contains "..", yet it points outside the share.
		link := filepath.Join(root, "escape")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		if _, err := resolver.Resolve("escape/secret.txt"); !errors.Is(err, security.ErrPathEscape) {
			t.Errorf("Resolve through escaping symlink returned %v, want ErrPathEscape", err)
		}
	})

	t.Run("allows symlinks staying inside the root", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation needs privileges on Windows")
		}
		link := filepath.Join(root, "alias.txt")
		if err := os.Symlink(filepath.Join(root, "inside.txt"), link); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		if _, err := resolver.Resolve("alias.txt"); err != nil {
			t.Errorf("Resolve of an internal symlink returned %v", err)
		}
	})
}

func TestArgon2HasherRoundTrip(t *testing.T) {
	// Cheap parameters: this test is about correctness, not about cost.
	hasher := security.NewArgon2Hasher(security.Argon2Params{Time: 1, Memory: 8 << 10, Threads: 1})

	hash, err := hasher.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if strings.Contains(hash, "correct horse") {
		t.Fatal("the hash contains the plaintext password")
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("hash %q is not in PHC argon2id format", hash)
	}

	if err := hasher.Verify("correct horse battery staple", hash); err != nil {
		t.Errorf("Verify with the right password returned %v", err)
	}
	if err := hasher.Verify("wrong", hash); !errors.Is(err, security.ErrPasswordMismatch) {
		t.Errorf("Verify with a wrong password returned %v, want ErrPasswordMismatch", err)
	}
}

func TestArgon2HashIsSalted(t *testing.T) {
	hasher := security.NewArgon2Hasher(security.Argon2Params{Time: 1, Memory: 8 << 10, Threads: 1})

	first, err := hasher.Hash("same")
	if err != nil {
		t.Fatal(err)
	}
	second, err := hasher.Hash("same")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("hashing the same password twice produced identical digests, so it is unsalted")
	}
}

func TestArgon2VerifyRejectsMalformedHashes(t *testing.T) {
	hasher := security.NewArgon2Hasher(security.DefaultArgon2Params())
	for _, broken := range []string{"", "plaintext", "$argon2i$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA", "$argon2id$x$y$z"} {
		if err := hasher.Verify("whatever", broken); err == nil {
			t.Errorf("Verify accepted malformed hash %q", broken)
		}
	}
}

func TestTokensAreUnguessable(t *testing.T) {
	source := security.NewCryptoTokenSource()

	ids := make(map[string]bool, 1000)
	tokens := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id, err := source.NewID()
		if err != nil {
			t.Fatal(err)
		}
		token, err := source.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if ids[id] {
			t.Fatalf("duplicate id %q after %d draws", id, i)
		}
		if tokens[token] {
			t.Fatalf("duplicate token %q after %d draws", token, i)
		}
		ids[id], tokens[token] = true, true

		// 16 random bytes in base64url is 22 characters; anything shorter
		// would mean the entropy floor slipped.
		if len(token) < 22 {
			t.Fatalf("token %q is only %d characters, want at least 22", token, len(token))
		}
		if strings.ContainsAny(id, "./+=") {
			t.Fatalf("id %q contains characters that are unsafe in a hostname", id)
		}
	}
}

func TestSignerRejectsForgedSignatures(t *testing.T) {
	keyA, _ := security.NewSecret()
	keyB, _ := security.NewSecret()
	signerA, signerB := security.NewHMACSigner(keyA), security.NewHMACSigner(keyB)

	signature := signerA.Sign("share-token")
	if err := signerA.Verify("share-token", signature); err != nil {
		t.Errorf("Verify of a valid signature returned %v", err)
	}
	if err := signerA.Verify("other-token", signature); !errors.Is(err, security.ErrBadSignature) {
		t.Error("a signature for one share verified against another")
	}
	if err := signerB.Verify("share-token", signature); !errors.Is(err, security.ErrBadSignature) {
		t.Error("a signature verified under a different key")
	}
}

func TestOpenSharedStaysOnTheApprovedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.txt")
	writeFile(t, path, "approved")

	f, err := security.OpenShared(path, "")
	if err != nil {
		t.Fatalf("OpenShared of the approved file: %v", err)
	}
	got := readAll(t, f)
	if got != "approved" {
		t.Errorf("OpenShared read %q, want the shared bytes", got)
	}

	// Replacing the file with another regular file is a live edit, not an escape.
	writeFile(t, path, "updated")
	f, err = security.OpenShared(path, "")
	if err != nil {
		t.Fatalf("OpenShared after an in-place replace: %v", err)
	}
	if got := readAll(t, f); got != "updated" {
		t.Errorf("OpenShared after replace read %q, want the new bytes", got)
	}
}

func TestOpenSharedRejectsASymlinkSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.txt")
	writeFile(t, path, "approved")
	secret := filepath.Join(t.TempDir(), "secret.txt")
	writeFile(t, secret, "classified")

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, path); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	f, err := security.OpenShared(path, "")
	if f != nil {
		f.Close()
	}
	if !errors.Is(err, security.ErrPathEscape) {
		t.Fatalf("OpenShared after a symlink swap returned %v, want ErrPathEscape", err)
	}
}

func TestOpenSharedConfinesDirectoryTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, filepath.Join(root, "inside.txt"), "ok")
	writeFile(t, filepath.Join(outside, "secret.txt"), "classified")
	link := filepath.Join(root, "escape.txt")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	if _, err := security.OpenShared(link, root); !errors.Is(err, security.ErrPathEscape) {
		t.Fatalf("OpenShared of an escaping symlink returned %v, want ErrPathEscape", err)
	}

	alias := filepath.Join(root, "alias.txt")
	if err := os.Symlink(filepath.Join(root, "inside.txt"), alias); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	f, err := security.OpenShared(alias, root)
	if err != nil {
		t.Fatalf("OpenShared of an internal symlink: %v", err)
	}
	if got := readAll(t, f); got != "ok" {
		t.Errorf("internal symlink read %q, want the file inside the share", got)
	}
}

func readAll(t *testing.T, f *os.File) string {
	t.Helper()
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}
