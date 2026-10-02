package server

import (
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AliJabbar034/vrok/internal/checksum"
	"github.com/AliJabbar034/vrok/internal/humanize"
	"github.com/AliJabbar034/vrok/internal/preview"
	"github.com/AliJabbar034/vrok/internal/security"
	"github.com/AliJabbar034/vrok/internal/sharing"
	"github.com/AliJabbar034/vrok/web/viewer"
)

// target is a resolved local file inside a share. Only this struct ever
// carries a filesystem path into the serving code, and it is only ever built
// by a handler that has already confined the path to the share.
type target struct {
	// Name is what the visitor sees.
	Name string
	// Rel is the path relative to the share root; "" for a single-file share.
	Rel string
	// Path is the absolute local path.
	Path string
	// Root is the directory the file must still live under. Empty means the
	// path itself is the approved file and must not have been swapped for a
	// symlink to something else.
	Root string
	Info fs.FileInfo
}

// assetServer streams local files and renders their preview pages. The three
// file-backed share kinds differ only in how they resolve a request to a
// target, so everything after that point is shared here.
type assetServer struct {
	detector  preview.Detector
	previews  *preview.Registry
	pages     *pages
	downloads downloadSessions
	clock     sharing.Clock
	checksums *checksum.Cache
}

// serve delivers a resolved target according to the request's delivery mode.
func (a *assetServer) serve(w http.ResponseWriter, r *http.Request, sr *shareRequest, t target) {
	switch sr.Delivery {
	case deliverInline, deliverAttachment:
		a.stream(w, r, sr, t)
	default:
		a.page(w, r, sr, t)
	}
}

// page renders the preview page for a file.
func (a *assetServer) page(w http.ResponseWriter, r *http.Request, sr *shareRequest, t target) {
	descriptor := a.detector.Detect(t.Name)
	asset := preview.Asset{
		Name:        t.Name,
		Descriptor:  descriptor,
		Size:        t.Info.Size(),
		ModTime:     t.Info.ModTime(),
		RawURL:      sr.Links.Raw(t.Rel),
		DownloadURL: sr.Links.Download(t.Rel),
		Content: preview.OpenerFunc(func() (preview.File, error) {
			return security.OpenShared(t.Path, t.Root)
		}),
	}

	data := viewer.FilePage{
		Meta:        a.pages.meta(sr, t.Name+" · vrok"),
		Name:        t.Name,
		Icon:        descriptor.Icon,
		SizeText:    humanize.Bytes(t.Info.Size()),
		DownloadURL: asset.DownloadURL,
		Preview:     a.previews.Render(asset),
	}
	switch sum, state := a.checksums.Lookup(t.Path, t.Root, t.Info); state {
	case checksum.Ready:
		data.SHA256 = sum
	case checksum.Pending:
		data.ChecksumPending = true
	}
	if t.Rel != "" {
		data.Breadcrumbs = crumbs(sr.Links.Breadcrumbs(rootLabel(sr.Spec), t.Rel))
	}

	noStore(w)
	if err := a.pages.render.File(w, http.StatusOK, data); err != nil {
		a.pages.serverError(w, r, err)
	}
}

// stream sends the file's bytes.
//
// Range, conditional requests, 206 responses and HEAD are all delegated to
// http.ServeContent, which is the only correct implementation of those
// semantics in the standard library. That is what gives video seeking and
// resumable downloads for free.
func (a *assetServer) stream(w http.ResponseWriter, r *http.Request, sr *shareRequest, t target) {
	// A request counts as a download unless it is a HEAD or continues one
	// already counted (see downloadSessions). The allowance is claimed before
	// the file is opened: refusing early is what makes --downloads a hard cap
	// even under concurrent requests.
	counted := r.Method != http.MethodHead && !a.downloads.Holds(r, sr.Spec, t.Rel)
	if counted {
		if _, err := sr.Share.ClaimDownload(sr.Now); err != nil {
			a.pages.gone(w, r, err)
			return
		}
	}

	f, err := security.OpenShared(t.Path, t.Root)
	if err != nil {
		if counted {
			sr.Share.ReleaseDownload()
		}
		a.notFoundOrError(w, r, err)
		return
	}
	defer f.Close()

	// Re-stat through the open descriptor so size and mtime describe the file
	// actually being sent, not what a directory listing claimed earlier.
	info, err := f.Stat()
	if err != nil {
		if counted {
			sr.Share.ReleaseDownload()
		}
		a.pages.serverError(w, r, err)
		return
	}

	descriptor := a.detector.Detect(t.Name)
	h := w.Header()
	// Setting Content-Type up front stops ServeContent from sniffing, so the
	// type is decided by vrok's table rather than by the file's first bytes.
	h.Set("Content-Type", descriptor.ContentType)
	h.Set("Content-Disposition", disposition(sr.Delivery, descriptor, t.Name))
	h.Set("X-Content-Type-Options", "nosniff")
	if sr.Spec.Kind != sharing.KindDirectory && scriptable(descriptor.ContentType) {
		// The preview page frames this file in a sandbox, but the raw URL
		// can also be opened directly. Without the header, a shared .html or
		// .svg opened that way would run script in vrok's own origin.
		// Directory shares are exempt by design: a built site or test report
		// needs its scripts (see docs/security.md).
		h.Set("Content-Security-Policy", "sandbox")
	}
	h.Set("ETag", etag(info))
	// Shares are short-lived but their contents are immutable for that window;
	// a private cache makes repeated video seeks cheap without risking the
	// file outliving the share in a shared cache.
	h.Set("Cache-Control", "private, max-age=0, must-revalidate")

	// Registered as in flight so the reaper does not stop the process while
	// the last permitted download is still being delivered.
	transfer := sr.Share.BeginTransfer()
	defer func() { transfer.End(a.clock.Now()) }()

	out := w
	var session *sessionWriter
	if counted {
		session = &sessionWriter{ResponseWriter: w, cookie: a.downloads.cookie(r, sr, t.Rel)}
		out = session
	}
	cw := &countingWriter{ResponseWriter: out, transfer: transfer}
	http.ServeContent(cw, r, t.Name, info.ModTime(), f)

	// A response that delivered nothing (a 304, a 416) must not consume the
	// visitor's allowance. Once a session has been issued it stays spent,
	// even if the client hangs up at once: the session is what lets it
	// resume, so releasing it too would turn hanging up into a free download.
	if counted && !session.issued {
		sr.Share.ReleaseDownload()
	}
}

// notFoundOrError answers a filesystem error on a path that was already
// resolved.
//
// Any failure to open or stat such a path is answered as not found: the file
// was deleted since the listing was rendered, or it became unreadable, or it
// turned into a symlink loop. None of those are vrok malfunctioning, and
// distinguishing them in the response would describe the filesystem to a
// visitor. A 500 is reserved for failures that are not about a path at all,
// such as a template that would not render.
func (a *assetServer) notFoundOrError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, security.ErrPathEscape) {
		a.pages.rejected(w, r, err)
		return
	}
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		a.pages.rejected(w, r, err)
		return
	}
	a.pages.serverError(w, r, err)
}

// listing renders a set of entries as a browsable index.
// maxListingEntries bounds how many entries one page renders.
//
// A listing is built in memory before it is written, so an unbounded one turns
// `vrok ./node_modules` into tens of megabytes of HTML per request. Entries
// are sorted before the cap is applied, so what a visitor sees is the
// alphabetical start of the directory rather than whatever order the
// filesystem happened to return.
const maxListingEntries = 2000

func (a *assetServer) listing(w http.ResponseWriter, r *http.Request, sr *shareRequest, heading string, entries []viewer.IndexEntry, bread []Crumb, current, archiveURL string) {
	total := len(entries)
	truncated := total > maxListingEntries
	if truncated {
		entries = entries[:maxListingEntries]
	}

	data := viewer.IndexPage{
		Meta:        a.pages.meta(sr, heading+" · vrok"),
		Heading:     heading,
		Current:     current,
		Breadcrumbs: crumbs(bread),
		Entries:     entries,
		Total:       total,
		Truncated:   truncated,
	}
	if total > 0 {
		data.ArchiveURL = archiveURL
	}
	noStore(w)
	if err := a.pages.render.Index(w, http.StatusOK, data); err != nil {
		a.pages.serverError(w, r, err)
	}
}

// disposition builds the Content-Disposition header, encoding the filename so
// that non-ASCII names survive.
func disposition(d delivery, desc preview.Descriptor, name string) string {
	kind := "attachment"
	if d == deliverInline && desc.Inline() {
		kind = "inline"
	}
	return mime.FormatMediaType(kind, map[string]string{"filename": name})
}

// scriptable reports whether a browser opening contentType at the top level
// would execute script embedded in it.
func scriptable(contentType string) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	switch mediaType {
	case "text/html", "application/xhtml+xml", "image/svg+xml", "text/xml", "application/xml":
		return true
	default:
		return false
	}
}

// etag derives a strong validator from size and modification time.
//
// It must be strong, not weak: http.ServeContent only honours If-Range with a
// strong validator, and without it an interrupted download cannot resume.
func etag(info fs.FileInfo) string {
	return fmt.Sprintf(`"%x-%x"`, info.Size(), info.ModTime().UnixNano())
}

func rootLabel(spec sharing.Spec) string {
	if spec.Kind == sharing.KindDirectory {
		return filepath.Base(spec.Root)
	}
	return spec.Name
}

// SingleFileHandler serves a share of exactly one file. The file is the share
// root, so there is nothing to navigate.
type SingleFileHandler struct{ *assetServer }

// ServeShare implements ShareHandler.
func (h SingleFileHandler) ServeShare(w http.ResponseWriter, r *http.Request, sr *shareRequest) {
	entry := sr.Spec.Entries[0]
	// The file answers at the share root and under its own name, so a link
	// that includes the filename (nicer when pasted into chat) also works.
	if sr.Rel != "" && sr.Rel != entry.Name {
		h.pages.gone(w, r, nil)
		return
	}

	info, err := os.Stat(entry.Path)
	if err != nil {
		h.notFoundOrError(w, r, err)
		return
	}
	h.serve(w, r, sr, target{Name: entry.Name, Rel: "", Path: entry.Path, Info: info})
}

// FileSetHandler serves several unrelated files through a generated index.
type FileSetHandler struct{ *assetServer }

// ServeShare implements ShareHandler.
func (h FileSetHandler) ServeShare(w http.ResponseWriter, r *http.Request, sr *shareRequest) {
	if sr.Rel == "" {
		if sr.Delivery == deliverArchive {
			h.streamZip(w, r, sr, sr.Spec.Name+".zip", entryMembers(sr.Spec.Entries))
			return
		}
		h.index(w, r, sr)
		return
	}

	// Only the names vrok generated are addressable. A visitor-supplied path
	// is matched against that list, never joined onto the filesystem.
	for _, entry := range sr.Spec.Entries {
		if entry.Name != sr.Rel {
			continue
		}
		info, err := os.Stat(entry.Path)
		if err != nil {
			h.notFoundOrError(w, r, err)
			return
		}
		h.serve(w, r, sr, target{Name: entry.Name, Rel: entry.Name, Path: entry.Path, Info: info})
		return
	}
	h.pages.gone(w, r, nil)
}

func (h FileSetHandler) index(w http.ResponseWriter, r *http.Request, sr *shareRequest) {
	entries := make([]viewer.IndexEntry, 0, len(sr.Spec.Entries))
	for _, e := range sr.Spec.Entries {
		size, modified := e.Size, ""
		if info, err := os.Stat(e.Path); err == nil {
			size = info.Size()
			modified = info.ModTime().Local().Format(time.DateTime)
		}
		entries = append(entries, viewer.IndexEntry{
			Name:        e.Name,
			URL:         sr.Links.Page(e.Name),
			DownloadURL: sr.Links.Download(e.Name),
			Icon:        h.detector.Detect(e.Name).Icon,
			SizeText:    humanize.Bytes(size),
			Modified:    modified,
		})
	}
	h.listing(w, r, sr, "Shared files", entries, nil, "", sr.Links.Archive(""))
}

// DirectoryHandler serves a directory tree, confined to its root.
type DirectoryHandler struct {
	*assetServer
	// roots confines each directory share. Resolvers are supplied by the
	// router so this handler never constructs a filesystem path itself.
	roots RootProvider
}

// RootProvider hands out the confined resolver for a share. The interface
// exists so the confinement strategy can be swapped (or stubbed in tests)
// without touching the handler.
type RootProvider interface {
	Resolver(spec sharing.Spec) (PathResolver, error)
}

// PathResolver maps an untrusted relative path to a real file inside a root.
type PathResolver interface {
	Root() string
	Resolve(rel string) (string, error)
}

// ServeShare implements ShareHandler.
func (h DirectoryHandler) ServeShare(w http.ResponseWriter, r *http.Request, sr *shareRequest) {
	resolver, err := h.roots.Resolver(sr.Spec)
	if err != nil {
		h.pages.serverError(w, r, err)
		return
	}

	abs, err := resolver.Resolve(sr.Rel)
	if err != nil {
		// Resolve is the boundary where untrusted input becomes a path, so
		// every failure here means the same thing to a visitor: there is
		// nothing at that address. An escape, a symlink loop and a typo must
		// be indistinguishable, or the difference becomes a probe.
		h.pages.rejected(w, r, err)
		return
	}

	info, err := os.Stat(abs)
	if err != nil {
		h.notFoundOrError(w, r, err)
		return
	}

	if info.IsDir() {
		if sr.Delivery == deliverArchive {
			h.archive(w, r, sr, resolver.Root(), abs)
			return
		}
		h.directory(w, r, sr, resolver.Root(), abs)
		return
	}
	h.serve(w, r, sr, target{Name: path.Base(sr.Rel), Rel: sr.Rel, Path: abs, Root: resolver.Root(), Info: info})
}

// archive streams a directory of the share, and everything under it, as one
// zip named after that directory.
func (h DirectoryHandler) archive(w http.ResponseWriter, r *http.Request, sr *shareRequest, root, abs string) {
	contained, err := security.Contained(abs, root)
	if err != nil {
		h.pages.rejected(w, r, err)
		return
	}
	name := rootLabel(sr.Spec)
	if sr.Rel != "" {
		name = path.Base(sr.Rel)
	}
	h.streamZip(w, r, sr, name+".zip", directoryMembers(contained, root, name))
}

// listParam forces the file listing for a directory that has an index.html.
const listParam = "list"

func (h DirectoryHandler) directory(w http.ResponseWriter, r *http.Request, sr *shareRequest, root, abs string) {
	// Relative links inside a listing only resolve correctly from a URL that
	// ends in a slash.
	if !strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, sr.Links.Page(sr.Rel+"/"), http.StatusMovedPermanently)
		return
	}

	// A directory with an index.html is almost always a built artefact — a
	// test report, a static site, a coverage summary — so serve it. ?list=1
	// still shows the raw contents.
	if !r.URL.Query().Has(listParam) {
		indexPath := filepath.Join(abs, "index.html")
		if info, err := os.Stat(indexPath); err == nil && info.Mode().IsRegular() {
			rel := path.Join(sr.Rel, "index.html")
			// The index is the page itself, so deliver bytes rather than a
			// preview page wrapped around it.
			if sr.Delivery == deliverPage {
				sr.Delivery = deliverInline
			}
			h.serve(w, r, sr, target{Name: "index.html", Rel: rel, Path: indexPath, Root: root, Info: info})
			return
		}
	}

	contained, err := security.Contained(abs, root)
	if err != nil {
		h.pages.rejected(w, r, err)
		return
	}
	dirEntries, err := os.ReadDir(contained)
	if err != nil {
		h.notFoundOrError(w, r, err)
		return
	}

	entries := make([]viewer.IndexEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		info, err := de.Info()
		if err != nil {
			continue // vanished between ReadDir and Info; just skip it
		}
		// Sockets, devices and fifos are not shareable content and would
		// block a reader forever if opened.
		if !info.Mode().IsRegular() && !de.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			continue
		}

		rel := path.Join(sr.Rel, de.Name())
		entry := viewer.IndexEntry{
			Name:     de.Name(),
			IsDir:    de.IsDir(),
			SizeText: humanize.Bytes(info.Size()),
			Modified: info.ModTime().Local().Format(time.DateTime),
		}
		if de.IsDir() {
			entry.Name += "/"
			entry.URL = sr.Links.Page(rel + "/")
			entry.Icon = "📁"
		} else {
			entry.URL = sr.Links.Page(rel)
			entry.DownloadURL = sr.Links.Download(rel)
			entry.Icon = h.detector.Detect(de.Name()).Icon
		}
		entries = append(entries, entry)
	}

	// Directories first, then files, each alphabetically: the order people
	// expect from every file browser.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	label := rootLabel(sr.Spec)
	heading := label + "/"
	current := ""
	var bread []Crumb
	if sr.Rel != "" {
		parts := splitPath(sr.Rel)
		current = parts[len(parts)-1] + "/"
		heading = label + "/" + strings.Join(parts, "/") + "/"
		// Breadcrumbs drop the final element, which the page shows as
		// Current, so the path can be passed through unchanged.
		bread = sr.Links.Breadcrumbs(label, sr.Rel)
	}
	h.listing(w, r, sr, heading, entries, bread, current, sr.Links.Archive(sr.Rel))
}
