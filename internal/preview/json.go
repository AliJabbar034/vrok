package preview

import (
	"html/template"
	"strings"
)

// highlightJSON wraps the tokens of already-indented JSON in span elements so
// the stylesheet can colour them.
//
// This is a lexer, not a parser: it only needs to classify tokens, and it must
// never fail on input the pretty-printer already accepted. Every literal is
// HTML-escaped on the way out, so the shared document cannot inject markup
// into the viewer page.
func highlightJSON(src string) template.HTML {
	var b strings.Builder
	b.Grow(len(src) * 2)

	for i := 0; i < len(src); {
		switch c := src[i]; {
		case c == '"':
			end := scanString(src, i)
			class := "str"
			if isObjectKey(src, end) {
				class = "key"
			}
			writeToken(&b, class, src[i:end])
			i = end

		case c == '-' || (c >= '0' && c <= '9'):
			end := scanNumber(src, i)
			writeToken(&b, "num", src[i:end])
			i = end

		case strings.HasPrefix(src[i:], "true"), strings.HasPrefix(src[i:], "false"):
			end := i + 4
			if src[i] == 'f' {
				end = i + 5
			}
			writeToken(&b, "bool", src[i:end])
			i = end

		case strings.HasPrefix(src[i:], "null"):
			writeToken(&b, "null", src[i:i+4])
			i += 4

		case strings.ContainsRune("{}[],:", rune(c)):
			writeToken(&b, "punc", src[i:i+1])
			i++

		default:
			// Indentation and newlines pass through untouched.
			b.WriteString(template.HTMLEscapeString(src[i : i+1]))
			i++
		}
	}
	return template.HTML(b.String())
}

func writeToken(b *strings.Builder, class, literal string) {
	b.WriteString(`<span class="tok-`)
	b.WriteString(class)
	b.WriteString(`">`)
	b.WriteString(template.HTMLEscapeString(literal))
	b.WriteString(`</span>`)
}

// scanString returns the index just past the closing quote of the string
// starting at the quote at position i.
func scanString(src string, i int) int {
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++ // skip the escaped byte
		case '"':
			return j + 1
		}
	}
	return len(src)
}

// scanNumber returns the index just past the numeric literal starting at i.
func scanNumber(src string, i int) int {
	j := i + 1
	for j < len(src) && strings.ContainsRune("0123456789.eE+-", rune(src[j])) {
		j++
	}
	return j
}

// isObjectKey reports whether the string ending at position end is followed by
// a colon, which is what distinguishes a key from a string value.
func isObjectKey(src string, end int) bool {
	for j := end; j < len(src); j++ {
		switch src[j] {
		case ' ', '\t', '\n', '\r':
			continue
		case ':':
			return true
		default:
			return false
		}
	}
	return false
}
