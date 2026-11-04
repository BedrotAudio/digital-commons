package attach

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testLimits = Limits{Extensions: []string{".txt", ".MD", ".csv", ".tsv"}, MaxBytes: 1024}

func write(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDecoding(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"ascii", []byte("hello"), "hello"},
		{"utf8", []byte("caf\xc3\xa9 \xe2\x82\xac"), "café €"},
		{"utf8 bom", []byte("\xef\xbb\xbfhello"), "hello"},
		{"empty", nil, ""},
		{"bom only", []byte("\xef\xbb\xbf"), ""},
		{"utf16 le", []byte{0xFF, 0xFE, 'h', 0, 'i', 0, 0xE9, 0}, "hié"},
		{"utf16 be", []byte{0xFE, 0xFF, 0, 'h', 0, 'i', 0, 0xE9}, "hié"},
		{"utf16 surrogate pair", []byte{0xFF, 0xFE, 0x3D, 0xD8, 0x00, 0xDE}, "\U0001F600"},
		{"utf16 odd trailing byte", []byte{0xFF, 0xFE, 'h', 0, 'i'}, "h"},
		{"utf16 crlf kept", []byte{0xFF, 0xFE, 'a', 0, '\r', 0, '\n', 0}, "a\r\n"},
		{"windows-1252", []byte("caf\xe9 \x80 \x93quoted\x94 \x96 na\xefve"), "café € \u201cquoted\u201d \u2013 naïve"},
		{"bad utf8 after bom", []byte("\xef\xbb\xbfcaf\xe9"), "café"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Read(write(t, "a.txt", c.data), testLimits)
			if err != nil {
				t.Fatal(err)
			}
			if got.Text != c.want {
				t.Fatalf("got %q, want %q", got.Text, c.want)
			}
		})
	}
}

func TestMediaTypeAndName(t *testing.T) {
	cases := []struct{ name, want string }{
		{"a.txt", "text/plain"},
		{"A.TXT", "text/plain"},
		{"notes.md", "text/markdown"},
		{"data.CSV", "text/csv"},
		{"data.tsv", "text/tab-separated-values"},
	}
	for _, c := range cases {
		got, err := Read(write(t, c.name, []byte("x")), testLimits)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.MediaType != c.want || got.Name != c.name {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
}

func TestRefusals(t *testing.T) {
	cases := []struct {
		name string
		file string
		data []byte
		want string
	}{
		{"pdf", "report.pdf", []byte("%PDF"), "report.pdf can't be attached. " + SaveAsText},
		{"docx", "report.docx", []byte("PK"), "report.docx can't be attached. " + SaveAsText},
		{"no extension", "README", []byte("x"), "README can't be attached. " + SaveAsText},
		{"binary renamed", "photo.txt", []byte("\x89PNG\r\n\x1a\n\x00\x00\x00"), "photo.txt doesn't look like a text file. " + SaveAsText},
		{"utf32 is binary", "w.txt", []byte{0xFF, 0xFE, 0, 0, 'h', 0, 0, 0}, "w.txt doesn't look like a text file. " + SaveAsText},
		{"too large", "big.txt", []byte(strings.Repeat("x", 1025)), "big.txt is too large to attach. The limit is 1 KB. " + SaveAsText},
		{"far too large", "huge.txt", []byte(strings.Repeat("x", 4096)), "huge.txt is too large to attach. The limit is 1 KB. " + SaveAsText},
		// Each byte becomes two when encoded, so this exceeds the limit only after decoding.
		{"grows when decoded", "w1252.txt", []byte(strings.Repeat("\xe9", 600)), "w1252.txt is too large to attach. The limit is 1 KB. " + SaveAsText},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Read(write(t, c.file, c.data), testLimits)
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("err = %v", err)
			}
			if got := Message(err); got != c.want {
				t.Fatalf("message = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSizeLimitIsOnEncodedText(t *testing.T) {
	// 700 characters are 1402 bytes of UTF-16 but only 700 bytes of UTF-8.
	data := []byte{0xFF, 0xFE}
	for range 700 {
		data = append(data, 'x', 0)
	}
	got, err := Read(write(t, "wide.txt", data), testLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Text) != 700 {
		t.Fatalf("length %d", len(got.Text))
	}

	exact := []byte(strings.Repeat("x", 1024))
	if _, err := Read(write(t, "exact.txt", exact), testLimits); err != nil {
		t.Fatalf("exactly at the limit: %v", err)
	}
}

func TestFolderAndMissingFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "folder.txt")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir, testLimits); !errors.Is(err, ErrRefused) {
		t.Fatalf("folder: %v", err)
	}
	_, err := Read(filepath.Join(t.TempDir(), "absent.txt"), testLimits)
	if err == nil || errors.Is(err, ErrRefused) {
		t.Fatalf("missing: %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cause lost: %v", err)
	}
}

func TestHostileNameIsCleaned(t *testing.T) {
	path := write(t, "report\u202egpj.txt", []byte("x"))
	got, err := Read(path, testLimits)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "reportgpj.txt" {
		t.Fatalf("name %q", got.Name)
	}
}
