package tunnel

import (
	"archive/tar"
	"compress/gzip"
	"context"
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
	if info, err := os.Stat(target); err == nil && info.Mode()&0o111 != 0 {
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

// fetch downloads the asset and writes the executable to target atomically,
// so an interrupted download cannot leave a half-written binary that a later
// run would happily execute.
func (p *provisioner) fetch(ctx context.Context, asset string, tarred bool, target string) error {
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

	body := io.LimitReader(resp.Body, maxDownloadSize)
	if tarred {
		return p.extract(body, target)
	}
	return writeExecutable(target, body)
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
