package components

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"digital-commons/terminal/internal/attach"
	"digital-commons/terminal/internal/security"
	"digital-commons/terminal/internal/theme"
	"digital-commons/terminal/internal/views"
)

// FileEntry is a row of the picker.
type FileEntry struct {
	Name string
	Dir  bool
}

// FilePicker is the data behind the file panel. bubbles/filepicker cannot show a
// drive row or filter case-insensitively, so this is small and purpose-built.
type FilePicker struct {
	Dir        string
	Entries    []FileEntry
	Extensions []string
	Drives     []string
	Need       int      // files to choose before closing
	Chosen     []string // names chosen so far
	Message    string   // a refusal or read failure, in plain language
}

// ReadDir lists a folder: subfolders first, then files of an accepted type,
// each sorted without regard to case. Hidden entries stay out of the way.
func ReadDir(dir string, extensions []string) ([]FileEntry, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var folders, files []FileEntry
	for _, item := range items {
		name := security.Clean(item.Name())
		if strings.HasPrefix(name, ".") || name != item.Name() {
			continue
		}
		isDir := item.IsDir()
		if item.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(dir, item.Name())); err == nil {
				isDir = info.IsDir()
			}
		}
		switch {
		case isDir:
			folders = append(folders, FileEntry{Name: name, Dir: true})
		case Accepted(name, extensions):
			files = append(files, FileEntry{Name: name})
		}
	}
	byName := func(a, b FileEntry) int {
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	}
	slices.SortFunc(folders, byName)
	slices.SortFunc(files, byName)
	return append(folders, files...), nil
}

// Accepted reports whether the name has an accepted extension, in any case.
func Accepted(name string, extensions []string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext != "" && slices.ContainsFunc(extensions, func(e string) bool {
		return strings.ToLower(e) == ext
	})
}

// Drives lists the drive roots on Windows and /Volumes on macOS, and nothing
// elsewhere.
func Drives(goos string) []string {
	if goos == "darwin" {
		return []string{"/Volumes"}
	}
	if goos != "windows" {
		return nil
	}
	return driveRoots()
}

// FirstEntry returns the control index of the first listed item, past the drive
// row and the way up, so that Enter opens something useful.
func (p *FilePicker) FirstEntry() int {
	n := len(p.Drives)
	if filepath.Dir(p.Dir) != p.Dir {
		n++
	}
	if len(p.Entries) == 0 {
		return max(n-1, 0)
	}
	return n
}

// Rows builds the panel rows for an inner width.
func (p *FilePicker) Rows(th theme.Theme, width int) []views.Row {
	st := th.Styles
	var rows []views.Row
	rows = append(rows, views.Row{{Text: views.TruncateStart(p.Dir, width), Style: st.Text}})

	if len(p.Drives) > 0 {
		row := views.Row{{Text: "Drives  ", Style: st.Muted}}
		for _, d := range p.Drives {
			name := strings.TrimRight(d, `\`)
			row = append(row, views.Control(" "+name+" ", st.Info, views.Action{Kind: "picker.drive", Arg: d}), views.Span{Text: " ", Style: st.Muted})
		}
		rows = append(rows, row)
	}
	if p.Message != "" {
		rows = append(rows, views.TextRows(p.Message, width, st.Warn)...)
	}
	rows = append(rows, views.Row{{Text: "", Style: st.Muted}})

	if parent := filepath.Dir(p.Dir); parent != p.Dir {
		rows = append(rows, views.Row{views.Control(views.ListRow("▸", "Up one folder", width), st.Info, views.Action{Kind: "picker.up"})})
	}
	if len(p.Entries) == 0 {
		rows = append(rows, views.Row{{Text: "  Nothing to attach in this folder.", Style: st.Muted}})
	}
	for i, e := range p.Entries {
		marker, name, style := " ", e.Name, st.Text
		if e.Dir {
			marker, name, style = "▸", e.Name+"/", st.Info
		}
		rows = append(rows, views.Row{views.Control(views.ListRow(marker, name, width), style, views.Action{Kind: "picker.entry", N: i})})
	}

	return rows
}

// Hints builds the rows that stay in sight however long the folder is.
func (p *FilePicker) Hints(th theme.Theme, width int) []views.Row {
	st := th.Styles
	rows := []views.Row{{{Text: "", Style: st.Muted}}}
	accepted := "Accepted: " + strings.Join(p.Extensions, " ")
	rows = append(rows, views.TextRows(accepted, width, st.Muted)...)
	rows = append(rows, views.TextRows(attach.SaveAsText, width, st.Muted)...)
	if p.Need > 1 {
		line := fmt.Sprintf("Choose %d files. %d chosen so far.", p.Need, len(p.Chosen))
		rows = append(rows, views.TextRows(line, width, st.Accent)...)
	}
	return rows
}
