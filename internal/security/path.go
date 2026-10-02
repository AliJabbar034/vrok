package security

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrPathEscape is returned when a request resolves outside the shared root.
// Callers must translate it into a 404 rather than a 403: telling a visitor
// that a path exists but is forbidden is already a disclosure.
var ErrPathEscape = errors.New("security: path escapes shared root")

// PathResolver maps an untrusted, request-supplied relative path onto a real
// file inside a single directory. It is the only component allowed to turn
// visitor input into a filesystem path.
//
// Confinement is enforced twice: lexically, by rejecting any path that climbs
// out of the root after cleaning, and physically, by resolving symlinks on the
// result and requiring it to still live under the root. The second check is
// what stops a symlink inside the share from pointing at /etc/passwd.
type PathResolver struct {
	// root is the fully resolved, symlink-free absolute shared directory.
	root string
	// followSymlinks allows links that stay inside the root. Links pointing
	// outside are rejected either way.
	followSymlinks bool
}

// NewPathResolver resolves root to a canonical absolute path and returns a
// resolver confined to it.
func NewPathResolver(root string) (*PathResolver, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("security: resolve root %q: %w", root, err)
	}
	// Canonicalise the root itself so a symlinked root (common on macOS,
	// where /tmp is a link to /private/tmp) does not make every lookup
	// below look like an escape.
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("security: resolve root %q: %w", root, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, fmt.Errorf("security: stat root %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("security: root %q is not a directory", root)
	}
	return &PathResolver{root: real, followSymlinks: true}, nil
}

// Root returns the canonical shared directory.
func (r *PathResolver) Root() string { return r.root }

// Resolve maps a URL-style relative path to an absolute path inside the root.
// The returned path is guaranteed to exist and to be contained by the root.
func (r *PathResolver) Resolve(rel string) (string, error) {
	cleaned, err := CleanRelative(rel)
	if err != nil {
		return "", err
	}

	candidate := filepath.Join(r.root, cleaned)

	// Containment before touching the disk: a lexical check cannot be fooled
	// by a missing file and costs nothing.
	if !contains(r.root, candidate) {
		return "", ErrPathEscape
	}

	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		// A broken or dangling link is indistinguishable from a missing file
		// as far as a visitor is concerned.
		return "", err
	}
	if !r.followSymlinks && real != candidate {
		return "", ErrPathEscape
	}
	// Containment after resolving links: this is the check that matters.
	if !contains(r.root, real) {
		return "", ErrPathEscape
	}
	return real, nil
}

// CleanRelative normalises an untrusted relative path and rejects anything
// that tries to climb above its own root. It operates purely on strings, so
// it is safe to call before any filesystem access.
func CleanRelative(rel string) (string, error) {
	// URL paths always use forward slashes; convert before cleaning so that
	// a backslash cannot smuggle a separator past us on Windows.
	rel = strings.ReplaceAll(rel, "\\", "/")
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return ".", nil
	}
	if strings.ContainsRune(rel, 0) {
		return "", ErrPathEscape
	}
	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", ErrPathEscape
	}
	if filepath.IsAbs(cleaned) {
		return "", ErrPathEscape
	}
	return cleaned, nil
}

// OpenShared opens path only after confirming it has not been redirected
// outside what the user approved.
//
// A share records a canonical path at creation time. Between then and a
// visitor's request the user (or something on the machine) could replace that
// path with a symlink to a secret. Opening blindly would serve the secret.
// This function re-resolves and refuses anything that is no longer the
// approved file, or — when root is set — anything that no longer lives under
// the shared directory.
func OpenShared(path, root string) (*os.File, error) {
	real, err := Contained(path, root)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(real)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	// A directory is opened only by the listing code, never here. A device
	// node or socket has no business being streamed as a share.
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, ErrPathEscape
	}
	return f, nil
}

// Contained re-resolves path and confirms it is still the approved location.
//
// For a single-file share (root empty) the last path component must still be
// a regular file: replacing it with a symlink after the share was created
// would otherwise redirect visitors to a secret. Intermediate directories
// may be OS-level links (macOS /var → /private/var); those are not a swap.
//
// For a directory share, the resolved path must still live under root.
func Contained(path, root string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 && root == "" {
		return "", ErrPathEscape
	}

	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if root != "" {
		// Canonicalise the root the same way NewPathResolver does. On macOS
		// /var is a link to /private/var; comparing the unresolved form
		// would treat every file inside a TempDir as an escape.
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return "", err
		}
		if !contains(realRoot, real) {
			return "", ErrPathEscape
		}
	}
	return real, nil
}

// contains reports whether child is root itself or lives beneath it.
func contains(root, child string) bool {
	if child == root {
		return true
	}
	return strings.HasPrefix(child, root+string(filepath.Separator))
}
