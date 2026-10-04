package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// maxArchiveSize caps a download. A vrok archive is around 10 MB; the cap
// keeps a redirect to something unexpected from filling memory.
const maxArchiveSize = 100 << 20

// ErrChecksumMismatch reports an archive that is not the published build.
// Nothing from it is written next to the binary.
var ErrChecksumMismatch = errors.New("update: downloaded archive does not match the published checksum")

// ErrNoSuchRelease reports a tag GitHub has no release for.
var ErrNoSuchRelease = errors.New("update: no such release")

// errNotFound marks a 404, which for checksums.txt means the tag itself does
// not exist.
var errNotFound = errors.New("not found")

// Apply downloads release tag for this platform, verifies it against the
// release's checksums.txt, and replaces the binary at exe with it.
//
// Verification fails closed, as install.sh does: anyone able to block
// checksums.txt could also swap the archive. The new binary is run once
// before it replaces the old one, so a truncated or wrong-platform build
// leaves the working install untouched.
func Apply(ctx context.Context, client *http.Client, tag, exe string) error {
	asset := archiveName(tag)
	base := releasesBase + "/download/" + tag + "/"

	sums, err := fetch(ctx, client, base+"checksums.txt", 1<<20)
	if errors.Is(err, errNotFound) {
		return fmt.Errorf("%w: %s", ErrNoSuchRelease, tag)
	}
	if err != nil {
		return fmt.Errorf("update: download checksums: %w", err)
	}
	want, err := checksumFor(sums, asset)
	if err != nil {
		return err
	}

	archive, err := fetch(ctx, client, base+asset, maxArchiveSize)
	if err != nil {
		return fmt.Errorf("update: download %s: %w", asset, err)
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("%w (%s: want %s, got %s)", ErrChecksumMismatch, asset, want, got)
	}

	binary, err := extract(archive, asset)
	if err != nil {
		return err
	}
	return replace(exe, binary, strings.TrimPrefix(tag, "v"))
}

func fetch(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", errNotFound, url)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", url, limit)
	}
	return body, nil
}

// checksumFor finds asset's SHA-256 in a GoReleaser checksums.txt, whose lines
// read "<hex>  <file name>".
func checksumFor(sums []byte, asset string) (string, error) {
	lines := bufio.NewScanner(bytes.NewReader(sums))
	for lines.Scan() {
		fields := strings.Fields(lines.Text())
		if len(fields) == 2 && fields[1] == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("update: checksums.txt has no entry for %s; this release may not publish a %s/%s build",
		asset, runtime.GOOS, runtime.GOARCH)
}

// extract pulls the CLI binary out of a release archive. Archives are flat,
// but the search matches on base name so a wrapping directory would not break
// updates.
func extract(archive []byte, asset string) ([]byte, error) {
	name := binaryName()
	if strings.HasSuffix(asset, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, fmt.Errorf("update: open %s: %w", asset, err)
		}
		for _, f := range zr.File {
			if archiveBase(f.Name) != name || f.FileInfo().IsDir() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxArchiveSize))
		}
		return nil, fmt.Errorf("update: %s does not contain %s", asset, name)
	}

	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("update: open %s: %w", asset, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("update: %s does not contain %s", asset, name)
		}
		if err != nil {
			return nil, fmt.Errorf("update: read %s: %w", asset, err)
		}
		if hdr.Typeflag == tar.TypeReg && archiveBase(hdr.Name) == name {
			return io.ReadAll(io.LimitReader(tr, maxArchiveSize))
		}
	}
}

// archiveBase is filepath.Base for archive paths, which always use slashes.
func archiveBase(name string) string {
	return name[strings.LastIndex(name, "/")+1:]
}

// replace swaps binary in at exe.
//
// The new file is written beside the old one, so the final step is a rename
// within one directory: atomic on Unix, and never a moment with no vrok at
// all. Windows will not overwrite a running executable but will rename one,
// so the running binary is moved aside to vrok.exe.old first; RemoveLeftover
// deletes it on a later run.
func replace(exe string, binary []byte, version string) error {
	dir := filepath.Dir(exe)
	pattern := ".vrok-update-*"
	if runtime.GOOS == "windows" {
		pattern += ".exe"
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return fmt.Errorf("update: write to %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // a no-op once renamed into place

	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()
		return fmt.Errorf("update: write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}

	if err := smokeTest(tmpName, version); err != nil {
		return err
	}

	if runtime.GOOS != "windows" {
		if err := os.Rename(tmpName, exe); err != nil {
			return fmt.Errorf("update: replace %s: %w", exe, err)
		}
		return nil
	}

	old := exe + ".old"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("update: move the running binary aside: %w", err)
	}
	if err := os.Rename(tmpName, exe); err != nil {
		// Put the working binary back rather than leave no vrok at all.
		_ = os.Rename(old, exe)
		return fmt.Errorf("update: replace %s: %w", exe, err)
	}
	return nil
}

// smokeTest runs the new binary's --version and checks it names the release.
// A build for the wrong platform, or a truncated one, fails here instead of
// after it has replaced a working install.
func smokeTest(path, version string) error {
	out, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("update: the downloaded vrok does not run on this machine: %w", err)
	}
	if !strings.Contains(string(out), version) {
		return fmt.Errorf("update: the downloaded vrok reports %q, not %s", strings.TrimSpace(string(out)), version)
	}
	return nil
}

// RemoveLeftover deletes the binary a previous Windows update moved aside.
// It is best-effort: the old file may still be running in another terminal.
func RemoveLeftover(exe string) {
	if runtime.GOOS == "windows" {
		_ = os.Remove(exe + ".old")
	}
}
