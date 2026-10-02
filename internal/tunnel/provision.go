package tunnel

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// cloudflaredRelease is the asset set vrok fetches when cloudflared is not
// installed. It is pinned rather than tracking "latest" so that the binary a
// user ends up running is the one this version of vrok was tested against,
// and so an upstream change cannot alter behaviour under them.
const cloudflaredRelease = "2025.8.1"

// downloadBase is where release assets come from. It is a variable so tests
// can point it at a local server instead of the internet.
var downloadBase = "https://github.com/cloudflare/cloudflared/releases/download"

// assetDigests pins the SHA-256 of every asset vrok may fetch for
// cloudflaredRelease. A download that does not match is discarded before it
// is written anywhere executable: HTTPS proves the bytes came from GitHub, not
// that they are the bytes this version of vrok was tested with.
//
// The values are GitHub's recorded asset digests. For the macOS archives they
// differ from the release notes, because Cloudflare re-uploaded notarised
// builds after publishing them. Bumping cloudflaredRelease means regenerating
// this table:
//
//	gh api repos/cloudflare/cloudflared/releases/tags/<version> \
//	  -q '.assets[] | "\(.name) \(.digest)"'
//
// It is a variable so tests can serve fake assets.
var assetDigests = map[string]string{
	"cloudflared-darwin-amd64.tgz":  "f64ad6bddc99053e2c69ff8ec40232bed7826ed495ecbd93be20632b7894b0a1",
	"cloudflared-darwin-arm64.tgz":  "2802da687e731e35bbf0edc86645ae6618135f0334c488dd7e0366c7f59ab1ed",
	"cloudflared-linux-386":         "5b40e1be2507185233108b9187fcff3b5edae44a8ba41249529b68c9d545e89c",
	"cloudflared-linux-amd64":       "a66353004197ee4c1fcb68549203824882bba62378ad4d00d234bdb8251f1114",
	"cloudflared-linux-arm":         "6ce1177f7f0384cf328a865cbcb1db4ad4353d4c64a60713fc7475965c16f9a8",
	"cloudflared-linux-arm64":       "9e2088063c8b8f71ce4b15d65e6f4b1ef345f90c9c15e762cfd2bc8fc63cf22a",
	"cloudflared-windows-386.exe":   "a1d3690cbda3ec76ca460bf23d3a85e59f4cc625e47da8bcd49cbb34863d717d",
	"cloudflared-windows-amd64.exe": "b5d598b00cc3a28cabc5812d9f762819334614bae452db4e7f23eefe7b081556",
}

// ErrChecksumMismatch reports a downloaded provider that is not the pinned
// build. Nothing from such a download is ever executed.
var ErrChecksumMismatch = errors.New("tunnel: downloaded cloudflared does not match its pinned checksum")

// maxDownloadSize caps what will be written to disk. cloudflared is around
// 40 MB; the cap exists so a redirect to something unexpected cannot fill the
// user's disk.
const maxDownloadSize = 150 << 20

// downloadTimeout bounds the whole fetch. A tunnel the user is waiting on is
// not worth an unbounded stall on a bad link.
const downloadTimeout = 3 * time.Minute

// ErrUnsupportedPlatform reports that vrok has no cloudflared build for this
// operating system and architecture.
var ErrUnsupportedPlatform = errors.New("tunnel: no cloudflared build for this platform")

// cloudflaredAsset names the release asset for the running platform and
// reports whether it arrives as a gzipped tar rather than a bare executable.
func cloudflaredAsset() (name string, tarred bool, err error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/amd64":
		return "cloudflared-darwin-amd64.tgz", true, nil
	case "darwin/arm64":
		return "cloudflared-darwin-arm64.tgz", true, nil
	case "linux/amd64":
		return "cloudflared-linux-amd64", false, nil
	case "linux/arm64":
		return "cloudflared-linux-arm64", false, nil
	case "linux/arm":
		return "cloudflared-linux-arm", false, nil
	case "linux/386":
		return "cloudflared-linux-386", false, nil
	case "windows/amd64":
		return "cloudflared-windows-amd64.exe", false, nil
	case "windows/386":
		return "cloudflared-windows-386.exe", false, nil
	default:
		return "", false, fmt.Errorf("%w (%s/%s)", ErrUnsupportedPlatform, runtime.GOOS, runtime.GOARCH)
	}
}

// cacheDir is where fetched binaries live, keyed by version so that upgrading
// vrok fetches afresh instead of reusing an older provider.
func cacheDir() (string, error) {
	base := os.Getenv("VROK_CACHE_DIR")
	if base == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("tunnel: locate cache directory: %w", err)
		}
		base = filepath.Join(dir, "vrok")
	}
	return filepath.Join(base, "bin", cloudflaredRelease), nil
}

// provisioner fetches cloudflared on demand and caches it.
//
// It exists so that the common case — a user who has installed nothing — still
// gets a public URL. Everything it does is announced: silently downloading and
// executing a binary is not something a tool should do behind someone's back.
type provisioner struct {
	logger *slog.Logger
	// notify reports progress to the user's terminal. The CLI supplies it;
	// when nil, provisioning is silent.
	notify func(format string, args ...any)
}

// executable returns a path to a runnable cloudflared, fetching it if needed.
func (p *provisioner) executable(ctx context.Context) (string, error) {
	asset, tarred, err := cloudflaredAsset()
	if err != nil {
		return "", err
	}

	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	name := "cloudflared"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(dir, name)

	// A cached copy from a previous share is the fast path, and the reason a
	// user pays the download once rather than every time.
	if info, err := os.Stat(target); err == nil && isCachedBinary(info) {
		p.logger.Debug("using cached provider", slog.String("path", target))
		return target, nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("tunnel: create %s: %w", dir, err)
	}

	p.announce("Setting up the tunnel provider (one-time, ~40 MB)...")
	if err := p.fetch(ctx, asset, tarred, target); err != nil {
		return "", err
	}
	p.announce("Tunnel provider ready.")
	return target, nil
}

func (p *provisioner) announce(format string, args ...any) {
	if p.notify != nil {
		p.notify(format, args...)
	}
}

// fetch downloads the asset, verifies it against its pinned digest and only
// then installs the executable at target. The download lands in a temporary
// file first, so an interrupted or tampered transfer never leaves anything a
// later run would find cached and execute.
func (p *provisioner) fetch(ctx context.Context, asset string, tarred bool, target string) error {
	want, ok := assetDigests[asset]
	if !ok {
		return fmt.Errorf("tunnel: no pinned checksum for %s", asset)
	}

	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	source := strings.Join([]string{downloadBase, cloudflaredRelease, asset}, "/")
	p.logger.Debug("fetching provider", slog.String("url", source))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("tunnel: download cloudflared: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tunnel: download cloudflared: %s returned %s", source, resp.Status)
	}

	download, err := os.CreateTemp(filepath.Dir(target), ".cloudflared-download-*")
	if err != nil {
		return fmt.Errorf("tunnel: create temporary file: %w", err)
	}
	defer os.Remove(download.Name())
	defer download.Close()

	digest := sha256.New()
	if _, err := io.Copy(io.MultiWriter(download, digest), io.LimitReader(resp.Body, maxDownloadSize)); err != nil {
		return fmt.Errorf("tunnel: download cloudflared: %w", err)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != want {
		return fmt.Errorf("%w (%s: got %s, want %s)", ErrChecksumMismatch, asset, got, want)
	}
	if _, err := download.Seek(0, io.SeekStart); err != nil {
		return err
	}

	if tarred {
		return p.extract(download, target)
	}
	return writeExecutable(target, download)
}

// isCachedBinary reports whether info is a previously fetched provider we can
// run. Unix execute bits are the real check; Windows does not persist them
// through Chmod, so a non-empty regular file is enough there.
func isCachedBinary(info os.FileInfo) bool {
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

// extract pulls the cloudflared executable out of a gzipped tar.
func (p *provisioner) extract(r io.Reader, target string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("tunnel: read cloudflared archive: %w", err)
	}
	defer gz.Close()

	archive := tar.NewReader(gz)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("tunnel: cloudflared archive contained no executable")
		}
		if err != nil {
			return fmt.Errorf("tunnel: read cloudflared archive: %w", err)
		}
		// Only the one known name is accepted, and only as a regular file.
		// Taking whatever the archive happens to contain, at whatever path it
		// names, is how archive extraction turns into arbitrary file write.
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "cloudflared" {
			continue
		}
		return writeExecutable(target, io.LimitReader(archive, maxDownloadSize))
	}
}

// writeExecutable writes r to path via a temporary file in the same
// directory, then renames it into place.
func writeExecutable(path string, r io.Reader) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".cloudflared-*")
	if err != nil {
		return fmt.Errorf("tunnel: create temporary file: %w", err)
	}
	defer os.Remove(temp.Name())

	if _, err := io.Copy(temp, r); err != nil {
		temp.Close()
		return fmt.Errorf("tunnel: write %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temp.Name(), 0o755); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return fmt.Errorf("tunnel: install %s: %w", path, err)
	}
	return nil
}
