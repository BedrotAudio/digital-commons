// Package attach reads a file the user selected and decodes it to UTF-8 text,
// because the gateway accepts attachments only as text.
package attach

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"digital-commons/terminal/internal/security"

	"golang.org/x/text/encoding/charmap"
)

// SaveAsText is the plain-language advice given with every refused file.
const SaveAsText = "Save PDF or Word files as text first."

// ErrRefused marks a file that cannot be attached, as opposed to one that could
// not be read. Its message is safe to show to any user.
var ErrRefused = errors.New("file refused")

// Limits mirrors the attachment limits from bootstrap.
type Limits struct {
	Extensions []string // lower or mixed case, with the leading dot
	MaxBytes   int      // limit on the UTF-8 encoded text
}

// File is a decoded attachment.
type File struct {
	Name      string // base name, cleaned for display
	MediaType string
	Text      string // always UTF-8
}

// Read checks and decodes the file at path. A refusal wraps ErrRefused.
func Read(path string, limits Limits) (File, error) {
	name := security.Clean(filepath.Base(path))

	if !allowed(name, limits.Extensions) {
		return File{}, refuse("%s can't be attached. %s", name, SaveAsText)
	}
	info, err := os.Stat(path)
	if err != nil {
		return File{}, fmt.Errorf("%s could not be read: %w", name, err)
	}
	if info.IsDir() {
		return File{}, refuse("%s is a folder. Choose a file.", name)
	}

	// The limit applies to the encoded text. UTF-16 can shrink to half its size
	// when encoded, so a file of up to twice the limit might still fit.
	ceiling := 2*int64(limits.MaxBytes) + 2
	if info.Size() > ceiling {
		return File{}, tooLarge(name, limits.MaxBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return File{}, fmt.Errorf("%s could not be read: %w", name, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, ceiling+1))
	if err != nil {
		return File{}, fmt.Errorf("%s could not be read: %w", name, err)
	}
	if int64(len(data)) > ceiling {
		return File{}, tooLarge(name, limits.MaxBytes)
	}

	text, err := decode(data)
	if err != nil {
		return File{}, fmt.Errorf("%s could not be read: %w", name, err)
	}
	if strings.ContainsRune(text, 0) {
		return File{}, refuse("%s doesn't look like a text file. %s", name, SaveAsText)
	}
	if len(text) > limits.MaxBytes {
		return File{}, tooLarge(name, limits.MaxBytes)
	}
	return File{Name: name, MediaType: mediaType(name), Text: text}, nil
}

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrRefused}, args...)...)
}

// Message returns the text to show for a Read error, without the internal prefix.
func Message(err error) string {
	return strings.TrimPrefix(err.Error(), ErrRefused.Error()+": ")
}

func tooLarge(name string, limit int) error {
	return refuse("%s is too large to attach. The limit is %d KB. %s", name, limit/1024, SaveAsText)
}

func allowed(name string, extensions []string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext != "" && slices.ContainsFunc(extensions, func(e string) bool {
		return strings.ToLower(e) == ext
	})
}

func mediaType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md":
		return "text/markdown"
	case ".csv":
		return "text/csv"
	case ".tsv":
		return "text/tab-separated-values"
	}
	return "text/plain"
}

// decode handles UTF-8 with or without a BOM, UTF-16 with a BOM, and otherwise
// Windows-1252, the usual encoding of older Windows files.
func decode(data []byte) (string, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		data = data[3:]
		if utf8.Valid(data) {
			return string(data), nil
		}
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return decodeUTF16(data[2:], false), nil
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return decodeUTF16(data[2:], true), nil
	case utf8.Valid(data):
		return string(data), nil
	}
	return charmap.Windows1252.NewDecoder().String(string(data))
}

func decodeUTF16(data []byte, bigEndian bool) string {
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		hi, lo := data[i+1], data[i]
		if bigEndian {
			hi, lo = lo, hi
		}
		units = append(units, uint16(hi)<<8|uint16(lo))
	}
	return string(utf16.Decode(units))
}
