package sharing

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Source is a validated description of what the user asked to share, before
// any ids, tokens or expiry are attached to it.
type Source struct {
	Kind    Kind
	Root    string
	Entries []Entry
	Target  string
	Name    string
}

// hostPort matches shorthand upstream addresses such as "localhost:3000" or
// "127.0.0.1:8080/base". A bare hostname without a port is not accepted: it
// would be impossible to tell from a mistyped filename.
var hostPort = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.\-_]*[A-Za-z0-9])?:\d{1,5}(/.*)?$`)

// Classify turns raw CLI arguments into a Source.
//
// Filesystem paths win over URL shorthand: a local file called "8080" is
// shared as a file, not mistaken for a port. Only when nothing exists on disk
// does vrok try to read the argument as an HTTP target.
func Classify(args []string) (Source, error) {
	switch len(args) {
	case 0:
		return Source{}, errors.New("sharing: nothing to share")
	case 1:
		return classifyOne(args[0])
	default:
		return classifyMany(args)
	}
}

func classifyOne(arg string) (Source, error) {
	info, err := os.Stat(arg)
	switch {
	case err == nil && info.IsDir():
		root, err := filepath.Abs(arg)
		if err != nil {
			return Source{}, fmt.Errorf("sharing: resolve %q: %w", arg, err)
		}
		return Source{Kind: KindDirectory, Root: root, Name: filepath.Base(root)}, nil

	case err == nil:
		entry, err := fileEntry(arg)
		if err != nil {
			return Source{}, err
		}
		return Source{Kind: KindFile, Entries: []Entry{entry}, Name: entry.Name}, nil

	case errors.Is(err, os.ErrNotExist):
		target, ok := ParseHTTPTarget(arg)
		if !ok {
			return Source{}, fmt.Errorf("sharing: %q is neither an existing path nor a host:port target", arg)
		}
		return Source{Kind: KindHTTP, Target: target, Name: target}, nil

	default:
		return Source{}, fmt.Errorf("sharing: inspect %q: %w", arg, err)
	}
}

func classifyMany(args []string) (Source, error) {
	// An unquoted name with spaces arrives as several arguments. If the first
	// token is not a file, try the words joined back together before failing.
	if _, err := os.Stat(args[0]); errors.Is(err, os.ErrNotExist) {
		joined := strings.Join(args, " ")
		if _, err := os.Stat(joined); err == nil {
			return classifyOne(joined)
		}
		return Source{}, fmt.Errorf("sharing: %q does not exist. If the name has spaces, quote it: vrok %q", args[0], joined)
	}

	entries := make([]Entry, 0, len(args))
	seen := make(map[string]int, len(args))

	for _, arg := range args {
		info, err := os.Stat(arg)
		if err != nil {
			return Source{}, fmt.Errorf("sharing: inspect %q: %w", arg, err)
		}
		if info.IsDir() {
			return Source{}, fmt.Errorf("sharing: %q is a directory; share a single directory on its own", arg)
		}
		entry, err := fileEntry(arg)
		if err != nil {
			return Source{}, err
		}
		// Two files from different directories can share a basename. Names are
		// the only handle a visitor gets, so they have to stay unique.
		if n, clash := seen[entry.Name]; clash {
			ext := filepath.Ext(entry.Name)
			entry.Name = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(entry.Name, ext), n+1, ext)
		}
		seen[entry.Name]++
		entries = append(entries, entry)
	}

	return Source{
		Kind:    KindFiles,
		Entries: entries,
		Name:    fmt.Sprintf("%d files", len(entries)),
	}, nil
}

func fileEntry(path string) (Entry, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Entry{}, fmt.Errorf("sharing: resolve %q: %w", path, err)
	}
	// Canonicalise now so that a later symlink swap cannot redirect the share
	// to a different file than the one the user approved.
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Entry{}, fmt.Errorf("sharing: resolve %q: %w", path, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return Entry{}, fmt.Errorf("sharing: stat %q: %w", path, err)
	}
	if info.IsDir() {
		return Entry{}, fmt.Errorf("sharing: %q is a directory", path)
	}
	if !info.Mode().IsRegular() {
		return Entry{}, fmt.Errorf("sharing: %q is not a regular file", path)
	}
	return Entry{Name: filepath.Base(real), Path: real, Size: info.Size()}, nil
}

// ParseHTTPTarget normalises the accepted shorthands for a local HTTP service
// into an absolute origin URL:
//
//	http://localhost:3000  ->  http://localhost:3000
//	localhost:3000         ->  http://localhost:3000
//	:3000                  ->  http://127.0.0.1:3000
//	3000                   ->  http://127.0.0.1:3000
func ParseHTTPTarget(arg string) (string, bool) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", false
	}

	if strings.HasPrefix(arg, "http://") || strings.HasPrefix(arg, "https://") {
		u, err := url.Parse(arg)
		if err != nil || u.Host == "" {
			return "", false
		}
		u.Path = strings.TrimSuffix(u.Path, "/")
		u.RawQuery, u.Fragment = "", ""
		return u.String(), true
	}

	if port, ok := parsePort(strings.TrimPrefix(arg, ":")); ok && !strings.Contains(arg, "/") {
		return fmt.Sprintf("http://127.0.0.1:%d", port), true
	}

	if hostPort.MatchString(arg) {
		u, err := url.Parse("http://" + arg)
		if err != nil || u.Host == "" {
			return "", false
		}
		if _, ok := parsePort(u.Port()); !ok {
			return "", false
		}
		u.Path = strings.TrimSuffix(u.Path, "/")
		return u.String(), true
	}

	return "", false
}

func parsePort(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	port, err := strconv.Atoi(s)
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}
