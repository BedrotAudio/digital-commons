package security

import (
	"net/url"
	"strings"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"LOCALHOST", true},
		{"LocalHost", true},
		{"127.0.0.1", true},
		{"127.5.5.5", true},
		{"::1", true},
		{"", false},
		{"127.0.0.1.evil.gov", false},
		{"localhost.evil.gov", false},
		{"evil-localhost", false},
		{"0.0.0.0", false},
		{"127.1", false},
		{"2130706433", false},
		{"0x7f.0.0.1", false},
		{"::", false},
		{"8.8.8.8", false},
		{"gateway.city.gov", false},
	}
	for _, c := range cases {
		if got := IsLoopbackHost(c.host); got != c.want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// A userinfo spoof must be judged by the real host, which url.Hostname returns.
func TestLoopbackThroughURLParsing(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"http://127.0.0.1@evil.gov/", false},
		{"http://localhost@evil.gov:8000/", false},
		{"http://evil.gov#@127.0.0.1/", false},
		{"http://127.0.0.1.evil.gov:8000", false},
		{"http://[::1]:8000", true},
		{"http://LOCALHOST:8000", true},
	}
	for _, c := range cases {
		u, err := url.Parse(c.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", c.raw, err)
		}
		if got := IsLoopbackHost(u.Hostname()); got != c.want {
			t.Errorf("%q: got %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestClean(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello world", "hello world"},
		{"keeps newline and tab", "a\n\tb", "a\n\tb"},
		{"keeps unicode", "café 日本語 ◆", "café 日本語 ◆"},
		{"keeps joiner", "a\u200db", "a\u200db"},
		{"clear screen", "a\x1b[2Jb", "ab"},
		{"color", "\x1b[31mred\x1b[0m", "red"},
		{"osc52 bel", "a\x1b]52;c;aGVsbG8=\x07b", "ab"},
		{"osc52 st", "a\x1b]52;c;aGVsbG8=\x1b\\b", "ab"},
		{"osc8", "\x1b]8;;http://evil.gov\x1b\\click\x1b]8;;\x1b\\", "click"},
		{"osc0 title", "a\x1b]0;owned\x07b", "ab"},
		{"csi c1", "a\u009b2Jb", "a2Jb"},
		{"osc c1", "a\u009d52;c;x\u009cb", "a52;c;xb"},
		{"bidi override", "a\u202eb", "ab"},
		{"bidi range", "\u202a\u202b\u202c\u202d\u202e", ""},
		{"isolates", "\u2066\u2067\u2068\u2069", ""},
		{"zero width", "a" + string(rune(0x200B)) + "b" + string(rune(0x2060)) + "c" + string(rune(0xFEFF)) + "d", "abcd"},
		{"tags", "a\U000E0041\U000E007Fb", "ab"},
		{"cr", "over\rwrite", "overwrite"},
		{"crlf", "a\r\nb", "a\nb"},
		{"backspace", "abc\x08\x08d", "abcd"},
		{"bel", "ding\x07", "ding"},
		{"del", "a\x7fb", "ab"},
		{"nul", "a\x00b", "ab"},
		{"lone esc", "a\x1b", "a"},
		{"dcs", "a\x1bPq#0\x1b\\b", "ab"},
		{"apc", "a\x1b_payload\x1b\\b", "ab"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Clean(c.in)
			if got != c.want {
				t.Fatalf("Clean(%q) = %q, want %q", c.in, got, c.want)
			}
			if again := Clean(got); again != got {
				t.Fatalf("not idempotent: %q then %q", got, again)
			}
		})
	}
}

// Decode-time cleaning sees fragments. Whatever survives must be plain text, and
// the accumulated text must stay free of control characters.
func TestCleanSplitAcrossDeltas(t *testing.T) {
	hostile := []string{
		"\x1b[2J", "\x1b]52;c;aGVsbG8=\x07", "\x1b]8;;http://evil.gov\x1b\\", "\x1b]0;x\x07", "\u009b2J",
	}
	for _, h := range hostile {
		for cut := 0; cut <= len(h); cut++ {
			first := "before" + h[:cut]
			second := h[cut:] + "after"
			got := Clean(Clean(first) + Clean(second))
			if strings.ContainsAny(got, "\x1b\x07\u009b\u009d") {
				t.Fatalf("control survived for %q cut %d: %q", h, cut, got)
			}
			if !strings.HasPrefix(got, "before") {
				t.Fatalf("lost text before the split: %q", got)
			}
		}
	}
}

func TestCleanPaste(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"crlf", "a\r\nb\r\n", "a\nb\n"},
		{"cr", "a\rb", "a\nb"},
		{"mixed", "a\r\n\r\nb\nc\rd", "a\n\nb\nc\nd"},
		{"escape", "a\r\n\x1b[2Jb", "a\nb"},
		{"tab", "a\tb", "a\tb"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CleanPaste(c.in); got != c.want {
				t.Fatalf("CleanPaste(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
