package ui

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

var notice = UpdateNotice{
	Current: "0.6.1",
	Latest:  "0.7.0",
	Command: "vrok update",
	URL:     "https://github.com/AliJabbar034/vrok/releases/tag/v0.7.0",
}

// Every row of the box must be the same width, or the right edge goes ragged.
func TestUpdateNoticeBoxIsRectangular(t *testing.T) {
	var out, errOut bytes.Buffer
	p := New(&out, &errOut)
	p.SetColor(false)
	p.UpdateNotice(notice, 200)

	if out.Len() != 0 {
		t.Errorf("notice wrote to stdout, where it would end up in a piped URL: %q", out.String())
	}
	rows := strings.Split(strings.Trim(errOut.String(), "\n"), "\n")
	if len(rows) != 7 {
		t.Fatalf("got %d rows, want 7:\n%s", len(rows), errOut.String())
	}
	width := utf8.RuneCountInString(rows[0])
	for _, row := range rows {
		if utf8.RuneCountInString(row) != width {
			t.Errorf("row %q is not %d wide", row, width)
		}
	}
	for _, want := range []string{"0.6.1 → 0.7.0", "vrok update", notice.URL} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("notice is missing %q", want)
		}
	}
}

func TestUpdateNoticeDropsTheBoxWhenTooNarrow(t *testing.T) {
	var errOut bytes.Buffer
	p := New(&bytes.Buffer{}, &errOut)
	p.SetColor(false)
	p.UpdateNotice(notice, 40)

	if strings.Contains(errOut.String(), "╭") {
		t.Errorf("drew a box that cannot fit 40 columns:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "vrok update") {
		t.Errorf("narrow notice lost the update command:\n%s", errOut.String())
	}
}

func TestShareBannerCarriesTheUpdateLine(t *testing.T) {
	var out bytes.Buffer
	p := New(&out, &bytes.Buffer{})
	p.SetColor(false)
	p.Started(ShareView{Name: "report.pdf", URL: "https://example.test/s/x", Update: &notice})

	if !strings.Contains(out.String(), "↑ Update available: 0.6.1 → 0.7.0 · run vrok update") {
		t.Errorf("banner has no update line:\n%s", out.String())
	}

	out.Reset()
	p.Started(ShareView{Name: "report.pdf", URL: "https://example.test/s/x"})
	if strings.Contains(out.String(), "Update available") {
		t.Errorf("banner mentioned an update with none known:\n%s", out.String())
	}
}
