// Package security holds the terminal's text and address safety checks.
package security

import (
	"net"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// IsLoopbackHost reports whether host, as returned by url.Hostname, names the
// local machine. It parses and never compares prefixes, so 127.0.0.1.evil.gov,
// localhost.evil.gov, 0.0.0.0, 127.1 and 2130706433 are all rejected.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Clean makes gateway text safe to display: it removes escape sequences, then
// control and invisible formatting characters, keeping newline and tab. It runs
// on every gateway string. Streamed replies are cleaned again as accumulated
// text, because a sequence can straddle two deltas.
func Clean(s string) string {
	return strings.Map(keep, ansi.Strip(s))
}

// CleanPaste normalizes CRLF and lone CR line endings to LF, then applies Clean.
func CleanPaste(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return Clean(s)
}

func keep(r rune) rune {
	switch {
	case r == '\n' || r == '\t':
		return r
	case unicode.IsControl(r): // C0, DEL and C1
		return -1
	case r >= 0x202A && r <= 0x202E: // bidi embedding and override
		return -1
	case r >= 0x2066 && r <= 0x2069: // bidi isolates
		return -1
	case r == 0x200B || r == 0x2060 || r == 0xFEFF:
		return -1
	case r >= 0xE0000 && r <= 0xE007F: // tag characters
		return -1
	}
	return r
}
