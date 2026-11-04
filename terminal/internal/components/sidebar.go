package components

import (
	"strings"
	"time"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/theme"
	"digital-commons/terminal/internal/views"

	"github.com/charmbracelet/x/ansi"
)

// Sidebar is the chat list. The cursor runs over every reachable row: New chat,
// the chats, then the bottom items.
type Sidebar struct {
	Chats   []client.ConversationSummary
	Current string // id of the open chat
	Cursor  int
	Offset  int  // first visible row of the scrolling part
	Dense   bool // Advanced mode: no group headings and no bottom items
	follow  bool // the cursor moved last, so the list must show it
}

type sideRow struct {
	heading string
	chat    int // index into Chats, -1 for a heading or spacer
	item    int // cursor index
}

var bottomItems = []struct{ label, kind string }{
	{"Privacy", "privacy"},
	{"Settings", "settings"},
	{"Help", "help"},
}

// ItemCount returns how many rows the cursor can reach.
func (s *Sidebar) ItemCount() int {
	n := 1 + len(s.Chats)
	if !s.Dense {
		n += len(bottomItems)
	}
	return n
}

// Move shifts the cursor and clamps it.
func (s *Sidebar) Move(delta int) {
	s.Cursor = min(max(s.Cursor+delta, 0), s.ItemCount()-1)
	s.follow = true
}

// CursorToCurrent places the cursor on the open chat, or on New chat.
func (s *Sidebar) CursorToCurrent() {
	s.Cursor = 0
	s.follow = true
	for i, chat := range s.Chats {
		if chat.ID == s.Current {
			s.Cursor = i + 1
		}
	}
}

// Scroll moves the list by whole rows.
func (s *Sidebar) Scroll(delta int) {
	s.Offset = max(s.Offset+delta, 0)
	s.follow = false
}

// CursorAction returns what Enter does on the cursor row.
func (s *Sidebar) CursorAction() views.Action {
	switch {
	case s.Cursor == 0:
		return views.Action{Kind: "new"}
	case s.Cursor <= len(s.Chats):
		return views.Action{Kind: "open", Arg: s.Chats[s.Cursor-1].ID}
	}
	return views.Action{Kind: bottomItems[s.Cursor-1-len(s.Chats)].kind}
}

// CursorChat returns the chat under the cursor, if any.
func (s *Sidebar) CursorChat() (client.ConversationSummary, bool) {
	if s.Cursor >= 1 && s.Cursor <= len(s.Chats) {
		return s.Chats[s.Cursor-1], true
	}
	return client.ConversationSummary{}, false
}

// Group names the history section for a time, relative to now in local time.
func Group(t, now time.Time) string {
	now = now.Local()
	t = t.Local()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch {
	case !t.Before(today):
		return "Today"
	case !t.Before(today.AddDate(0, 0, -1)):
		return "Yesterday"
	case !t.Before(today.AddDate(0, 0, -7)):
		return "Previous 7 days"
	}
	return "Older"
}

func (s *Sidebar) rows(now time.Time) []sideRow {
	var rows []sideRow
	last := ""
	for i, chat := range s.Chats {
		if !s.Dense {
			group := "Older"
			if t, err := client.ParseTime(chat.UpdatedAt); err == nil {
				group = Group(t, now)
			}
			if group != last {
				if last != "" {
					rows = append(rows, sideRow{chat: -1})
				}
				rows = append(rows, sideRow{heading: group, chat: -1})
				last = group
			}
		}
		rows = append(rows, sideRow{chat: i, item: i + 1})
	}
	return rows
}

// SideOut is a drawn sidebar. Zones are relative to its top-left corner.
type SideOut struct {
	Content string
	Zones   []views.Zone
	Rows    []SideHighlight
}

// SideHighlight marks a row for a raised background.
type SideHighlight struct{ Y int }

// Render draws the sidebar in width by height cells. Dense mode omits the brand
// row, because the Advanced header carries it.
func (s *Sidebar) Render(th theme.Theme, width, height int, focused bool, now time.Time) SideOut {
	inner := width - 2
	var lines []string
	var out SideOut
	st := th.Styles

	if !s.Dense {
		lines = append(lines, st.Accent.Render("◆")+" "+st.Text.Bold(true).Render(views.Truncate("Digital Commons", inner-2)), "")
	}

	newLabel := "+ New chat"
	if s.Dense {
		newLabel = "+ new"
	}
	newStyle := st.Accent.Bold(true)
	if focused && s.Cursor == 0 {
		newLabel = "▸ " + strings.TrimPrefix(newLabel, "+ ")
	}
	out.Zones = append(out.Zones, views.Zone{X: 0, Y: len(lines), W: width, H: 1, Action: views.Action{Kind: "new"}})
	if focused && s.Cursor == 0 {
		out.Rows = append(out.Rows, SideHighlight{Y: len(lines)})
	}
	lines = append(lines, newStyle.Render(newLabel))
	if !s.Dense {
		lines = append(lines, "")
	}

	bottom := 0
	if !s.Dense {
		bottom = len(bottomItems) + 1
	}
	listHeight := max(height-len(lines)-bottom, 1)
	rows := s.rows(now)

	if s.follow && s.Cursor >= 1 && s.Cursor <= len(s.Chats) {
		for i, r := range rows {
			if r.item == s.Cursor {
				if i < s.Offset {
					s.Offset = i
				}
				if i >= s.Offset+listHeight {
					s.Offset = i - listHeight + 1
				}
			}
		}
	}
	s.Offset = min(s.Offset, max(len(rows)-listHeight, 0))

	if len(rows) == 0 {
		for _, line := range views.Wrap("No chats yet", inner) {
			lines = append(lines, st.Muted.Render(line))
		}
	}
	for i := s.Offset; i < min(s.Offset+listHeight, len(rows)); i++ {
		r := rows[i]
		y := len(lines)
		if r.chat < 0 {
			lines = append(lines, st.Heading.Render(views.Truncate(r.heading, inner)))
			continue
		}
		chat := s.Chats[r.chat]
		selected := focused && s.Cursor == r.item
		current := chat.ID == s.Current
		marker := " "
		switch {
		case selected:
			marker = "▸"
		case current && focused:
			marker = "·"
		case current:
			marker = "▸"
		}
		title := chat.Title
		if title == "" {
			title = "New chat"
		}
		active := selected || (current && !focused)
		labelWidth := inner
		if active {
			labelWidth = inner - 2
		}
		label := views.ListRow(marker, title, labelWidth)
		style := st.Text
		if !selected && !current {
			style = st.Muted
		}
		if selected {
			style = st.Text.Bold(true)
		}
		out.Zones = append(out.Zones, views.Zone{X: 0, Y: y, W: width, H: 1, Action: views.Action{Kind: "open", Arg: chat.ID}})
		text := style.Render(label)
		if active {
			text += " " + st.Muted.Render("×")
			out.Zones = append(out.Zones, views.Zone{X: 1 + inner - 1, Y: y, W: 1, H: 1, Action: views.Action{Kind: "delete-chat", Arg: chat.ID}})
		}
		if selected || current {
			out.Rows = append(out.Rows, SideHighlight{Y: y})
		}
		lines = append(lines, text)
	}

	if !s.Dense {
		for len(lines) < height-len(bottomItems) {
			lines = append(lines, "")
		}
		lines = lines[:min(len(lines), max(height-len(bottomItems), 0))]
		for i, item := range bottomItems {
			y := len(lines)
			selected := focused && s.Cursor == 1+len(s.Chats)+i
			marker := " "
			style := st.Muted
			if selected {
				marker, style = "▸", st.Text.Bold(true)
				out.Rows = append(out.Rows, SideHighlight{Y: y})
			}
			out.Zones = append(out.Zones, views.Zone{X: 0, Y: y, W: width, H: 1, Action: views.Action{Kind: item.kind}})
			lines = append(lines, style.Render(views.ListRow(marker, item.label, inner)))
		}
	}

	for i, line := range lines {
		if ansi.StringWidth(line) > inner {
			line = ansi.Truncate(line, inner, "")
		}
		lines[i] = " " + line
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	out.Content = strings.Join(lines, "\n")
	return out
}
