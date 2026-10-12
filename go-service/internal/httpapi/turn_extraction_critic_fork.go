package httpapi

import (
	"fmt"
	"strings"
)

// escapeJSONControlCharactersInStrings escapes raw control characters, such
// as a line break copied from a multi-line quotation, inside string literals.
// JSON never allows them there, so only otherwise invalid text changes.
func escapeJSONControlCharactersInStrings(input string) string {
	var b strings.Builder
	b.Grow(len(input))
	inString, escaped := false, false
	for i := 0; i < len(input); i++ {
		ch := input[i]
		if !inString {
			if ch == '"' {
				inString = true
			}
			b.WriteByte(ch)
			continue
		}
		switch {
		case escaped:
			escaped = false
		case ch == '\\':
			escaped = true
		case ch == '"':
			inString = false
		case ch < 0x20:
			switch ch {
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				fmt.Fprintf(&b, `\u%04x`, ch)
			}
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}
