package inbox

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxNameBytes keeps a name well inside every filesystem's 255-byte limit,
// leaving room for the " (12)" a duplicate gets.
const maxNameBytes = 200

// windowsReserved are device names Windows refuses as file names, with or
// without an extension.
var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SafeName turns a name a visitor sent into one that is safe to create in
// the inbox on any platform. It keeps only the last path element, so no name
// can point outside the folder, and drops anything that would make the file
// hidden, unopenable, or a device on Windows. The result is never empty.
func SafeName(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToValidUTF8(name, "")

	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsControl(r) || isBidiControl(r):
			continue
		case strings.ContainsRune(`<>:"|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	// A leading dot hides a file, and Windows strips trailing dots and
	// spaces itself, which would make two different names collide.
	name = strings.TrimLeft(strings.TrimSpace(b.String()), ".")
	name = strings.TrimRight(name, ". ")

	base := strings.ToUpper(strings.TrimSuffix(name, filepath.Ext(name)))
	if windowsReserved[base] {
		name = "_" + name
	}
	name = truncate(name, maxNameBytes)
	if name == "" {
		return "file"
	}
	return name
}

// isBidiControl reports the invisible characters that reorder text. In a
// name they can make "evil\u202egpj.exe" display as "evilexe.jpg", both in
// the owner's terminal and in their file manager.
func isBidiControl(r rune) bool {
	switch {
	case r == 0x061c, r == 0x200e, r == 0x200f:
		return true
	case r >= 0x202a && r <= 0x202e:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// truncate shortens name to at most limit bytes, keeping its extension and
// never cutting a character in half.
func truncate(name string, limit int) string {
	if len(name) <= limit {
		return name
	}
	ext := filepath.Ext(name)
	if len(ext) > 16 {
		ext = ""
	}
	stem := name[:len(name)-len(ext)]
	keep := limit - len(ext)
	for keep > 0 && !utf8.RuneStart(stem[keep]) {
		keep--
	}
	return stem[:keep] + ext
}

// numbered returns the n-th alternative for a name that is already taken:
// "photo.jpg" becomes "photo (1).jpg", as a browser names a second download.
func numbered(name string, n int) string {
	ext := filepath.Ext(name)
	if ext == name {
		ext = ""
	}
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), n, ext)
}
