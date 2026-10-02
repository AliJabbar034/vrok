package preview_test

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AliJabbar034/vrok/internal/preview"
)

func TestDetectorClassifiesByExtension(t *testing.T) {
	detector := preview.NewDetector()

	cases := []struct {
		name        string
		kind        preview.Kind
		contentType string
	}{
		{"clip.mp4", preview.KindVideo, "video/mp4"},
		{"CLIP.MOV", preview.KindVideo, "video/quicktime"},
		{"photo.jpeg", preview.KindImage, "image/jpeg"},
		{"icon.svg", preview.KindImage, "image/svg+xml"},
		{"report.pdf", preview.KindPDF, "application/pdf"},
		{"notes.md", preview.KindMarkdown, ""},
		{"data.json", preview.KindJSON, "application/json"},
		{"page.html", preview.KindHTML, ""},
		{"bundle.zip", preview.KindArchive, "application/zip"},
		{"disk.iso", preview.KindArchive, ""},
		{"server.log", preview.KindText, ""},
		{"main.go", preview.KindText, ""},
		{"Dockerfile", preview.KindText, ""},
		{"Makefile", preview.KindText, ""},
		{"CHANGELOG", preview.KindText, ""},
		{"LICENSE", preview.KindText, ""},
		{"firmware.bin", preview.KindBinary, "application/octet-stream"},

		// Office formats are zip containers, but listing their internals
		// would be useless, so they are offered as downloads.
		{"quarterly.xlsx", preview.KindBinary, ""},
		{"contract.docx", preview.KindBinary, ""},

		// Disk images are archives by kind, which is enough to mark them
		// download-only; only zips actually get a listing.
		{"installer.dmg", preview.KindArchive, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := detector.Detect(tc.name)
			if got.Kind != tc.kind {
				t.Errorf("Detect(%q).Kind = %q, want %q", tc.name, got.Kind, tc.kind)
			}
			if tc.contentType != "" && got.ContentType != tc.contentType {
				t.Errorf("Detect(%q).ContentType = %q, want %q", tc.name, got.ContentType, tc.contentType)
			}
			if got.Icon == "" {
				t.Errorf("Detect(%q) has no icon", tc.name)
			}
		})
	}
}

// The preview table decides how a file is presented, never whether it can be
// shared. Anything unrecognised must still classify cleanly and get a
// Content-Type, because an unknown extension is not an error.
func TestUnknownTypesStillClassify(t *testing.T) {
	detector := preview.NewDetector()
	registry := preview.NewRegistry()

	for _, name := range []string{
		"mystery.qqq", "firmware.v2.7.blob", "no-extension-at-all",
		"données-été.dat", "report final (v2).whatever", "archive.tar.zst",
		".hidden", "trailing.", "UPPER.CASE.XYZ",
	} {
		t.Run(name, func(t *testing.T) {
			got := detector.Detect(name)
			if got.ContentType == "" {
				t.Errorf("Detect(%q) returned no Content-Type", name)
			}
			if got.Kind == "" {
				t.Errorf("Detect(%q) returned no Kind", name)
			}
			// A renderer must always be found, or the viewer would have
			// nothing to show for a file type nobody anticipated.
			asset := preview.Asset{Name: name, Descriptor: got, Size: 1024}
			if html := registry.Render(asset); html == "" {
				t.Errorf("Render(%q) produced nothing, want a download card", name)
			}
		})
	}
}

func TestInlineDispositionRules(t *testing.T) {
	detector := preview.NewDetector()
	// Media and documents display; archives and opaque binaries download,
	// because a browser rendering a disk image helps nobody.
	for _, name := range []string{"clip.mp4", "photo.png", "report.pdf", "notes.md", "page.html"} {
		if !detector.Detect(name).Inline() {
			t.Errorf("%s should be served inline", name)
		}
	}
	for _, name := range []string{"bundle.zip", "disk.iso", "firmware.bin"} {
		if detector.Detect(name).Inline() {
			t.Errorf("%s should be served as an attachment", name)
		}
	}
}

// asset builds a previewable asset backed by a real file.
func asset(t *testing.T, name, content string) preview.Asset {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return preview.Asset{
		Name:        name,
		Descriptor:  preview.NewDetector().Detect(name),
		Size:        info.Size(),
		ModTime:     info.ModTime(),
		RawURL:      "/s/tok/" + name + "?raw=1",
		DownloadURL: "/s/tok/" + name + "?dl=1",
		Content: preview.OpenerFunc(func() (preview.File, error) {
			return os.Open(path)
		}),
	}
}

func TestRenderersProduceTheExpectedMarkup(t *testing.T) {
	registry := preview.NewRegistry()

	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{"clip.mp4", "fake-video", []string{"<video", "controls", "?raw=1"}},
		{"photo.png", "fake-png", []string{"<img", "?raw=1"}},
		{"report.pdf", "%PDF-", []string{"<object", "application/pdf"}},
		{"song.mp3", "fake-audio", []string{"<audio", "controls"}},
		{"page.html", "<p>hi</p>", []string{"<iframe", "sandbox"}},
		{"notes.md", "# Heading\n\n- item\n", []string{"<h1", "Heading", "<li>"}},
		{"notes.txt", "plain text", []string{"<pre", "plain text"}},
		{"firmware.bin", "\x00\x01", []string{"Download", "?dl=1"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			html := string(registry.Render(asset(t, tc.name, tc.content)))
			for _, want := range tc.want {
				if !strings.Contains(html, want) {
					t.Errorf("rendered %s does not contain %q:\n%s", tc.name, want, html)
				}
			}
		})
	}
}

func TestPreviewsEscapeFileContent(t *testing.T) {
	registry := preview.NewRegistry()

	// A shared file is untrusted input as far as the viewer page is
	// concerned; nothing inside it may become live markup.
	t.Run("text", func(t *testing.T) {
		html := string(registry.Render(asset(t, "evil.txt", `<script>alert(1)</script>`)))
		if strings.Contains(html, "<script>alert(1)</script>") {
			t.Error("text preview emitted the file's script tag unescaped")
		}
		if !strings.Contains(html, "&lt;script&gt;") {
			t.Error("text preview did not escape the content")
		}
	})

	t.Run("markdown drops raw html", func(t *testing.T) {
		html := string(registry.Render(asset(t, "evil.md", "# ok\n\n<script>alert(1)</script>\n")))
		if strings.Contains(html, "<script>") {
			t.Error("markdown preview passed through a raw script tag")
		}
	})

	t.Run("json", func(t *testing.T) {
		html := string(registry.Render(asset(t, "evil.json", `{"x":"<img src=x onerror=alert(1)>"}`)))
		if strings.Contains(html, "<img src=x") {
			t.Error("json preview emitted the file's markup unescaped")
		}
	})
}

func TestJSONPreviewHighlightsTokens(t *testing.T) {
	registry := preview.NewRegistry()
	html := string(registry.Render(asset(t, "data.json",
		`{"name":"vrok","count":42,"ok":true,"missing":null,"tags":["a"]}`)))

	for _, class := range []string{"tok-key", "tok-str", "tok-num", "tok-bool", "tok-null", "tok-punc"} {
		if !strings.Contains(html, class) {
			t.Errorf("highlighted JSON has no %s span", class)
		}
	}
	// Highlighting happens server-side precisely so the page needs no script.
	if strings.Contains(html, "<script") {
		t.Error("the JSON preview pulled in a script")
	}
}

func TestInvalidJSONFallsBackToText(t *testing.T) {
	registry := preview.NewRegistry()
	html := string(registry.Render(asset(t, "broken.json", `{"unclosed": `)))

	if !strings.Contains(html, "Not valid JSON") {
		t.Error("an unparseable JSON file was not reported as such")
	}
	if !strings.Contains(html, "unclosed") {
		t.Error("the raw content was not shown as a fallback")
	}
}

func TestArchivePreviewListsEntriesWithoutExtracting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.zip")

	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, name := range []string{"README.md", "src/main.go"} {
		w, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("content of " + name))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	file.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(preview.NewRegistry().Render(preview.Asset{
		Name:       "bundle.zip",
		Descriptor: preview.NewDetector().Detect("bundle.zip"),
		Size:       info.Size(),
		Content: preview.OpenerFunc(func() (preview.File, error) {
			return os.Open(path)
		}),
	}))

	for _, want := range []string{"README.md", "src/main.go", "2 entries"} {
		if !strings.Contains(html, want) {
			t.Errorf("the archive listing does not contain %q", want)
		}
	}
}

// Only real zips are opened, and a zip that cannot be read must still be
// downloadable. This is the one preview path that relies on the registry
// swallowing a renderer error rather than on a filename check, so a visitor
// seeing a 500 instead of a download button would be a regression.
func TestUnreadableArchivesDegradeToADownload(t *testing.T) {
	dir := t.TempDir()

	cases := map[string][]byte{
		// A truncated zip: the header looks right, the central directory
		// is missing.
		"truncated.zip": []byte("PK\x03\x04 this is not a complete archive"),
		// Formats classified as archives that are not zips at all. These are
		// rejected by Supports, before any parsing.
		"installer.dmg": []byte("not a zip"),
		"ubuntu.iso":    []byte("not a zip either"),
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}

			html := string(preview.NewRegistry().Render(preview.Asset{
				Name:        name,
				Descriptor:  preview.NewDetector().Detect(name),
				Size:        int64(len(content)),
				DownloadURL: "/s/token/" + name + "?dl=1",
				Content: preview.OpenerFunc(func() (preview.File, error) {
					return os.Open(path)
				}),
			}))

			if !strings.Contains(html, "Download") {
				t.Errorf("%s was not offered as a download:\n%s", name, html)
			}
		})
	}
}

// Office formats are zip containers, but their internals are an
// implementation detail: a listing of word/document.xml tells a visitor
// nothing about the document.
func TestOfficeDocumentsAreNotListedAsArchives(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"contract.docx", "quarterly.xlsx", "deck.pptx"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(file)
			inner, err := writer.Create("word/document.xml")
			if err != nil {
				t.Fatal(err)
			}
			inner.Write([]byte("<w:document/>"))
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			file.Close()

			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			html := string(preview.NewRegistry().Render(preview.Asset{
				Name:        name,
				Descriptor:  preview.NewDetector().Detect(name),
				Size:        info.Size(),
				DownloadURL: "/s/token/" + name + "?dl=1",
				Content: preview.OpenerFunc(func() (preview.File, error) {
					return os.Open(path)
				}),
			}))

			if strings.Contains(html, "word/document.xml") {
				t.Errorf("%s leaked its internal structure into the preview", name)
			}
			if !strings.Contains(html, "Download") {
				t.Errorf("%s was not offered as a download", name)
			}
		})
	}
}

func TestOversizedTextFilesDegradeToADownload(t *testing.T) {
	// Rendering must never mean reading an unbounded amount into memory, so
	// large text files fall back to the download card.
	big := strings.Repeat("x", (2<<20)+1)
	html := string(preview.NewRegistry().Render(asset(t, "huge.txt", big)))

	if !strings.Contains(html, "Download") {
		t.Error("an oversized text file was not offered as a download")
	}
	if strings.Contains(html, strings.Repeat("x", 1000)) {
		t.Error("an oversized text file was inlined into the page")
	}
}

func TestRegistryWithAddsHigherPriorityRenderers(t *testing.T) {
	custom := stubRenderer{}
	registry := preview.NewRegistry().With(custom)

	if got := string(registry.Render(asset(t, "clip.mp4", "data"))); got != "<custom>" {
		t.Errorf("Render = %q, want the registered renderer to win", got)
	}
	// The original registry must be unaffected: With returns a copy.
	if got := string(preview.NewRegistry().Render(asset(t, "clip.mp4", "data"))); strings.Contains(got, "custom") {
		t.Error("With mutated the registry it was called on")
	}
}
