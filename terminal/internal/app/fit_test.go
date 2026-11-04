package app

import (
	"fmt"
	"github.com/charmbracelet/colorprofile"
	"strings"
	"testing"
	"time"

	"digital-commons/terminal/internal/views"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Every size the task names, and each edge of the responsive table.
var fitSizes = [][2]int{
	{30, 8}, {40, 12}, {60, 20}, {80, 24}, {100, 30}, {120, 40}, {160, 50}, {200, 60},
	{40, 10}, {39, 10}, {40, 9}, {59, 19}, {60, 13}, {89, 14}, {90, 20}, {119, 19}, {120, 13},
}

// assertFits checks one frame: exact height, nothing too wide, a cursor inside the
// window, and the parts a person needs still on screen.
func assertFits(t *testing.T, d *driver, state string, w, h int) {
	t.Helper()
	m := d.m
	view := m.View()
	label := fmt.Sprintf("%s %s %dx%d", modeName(m.advanced()), state, w, h)
	lines := strings.Split(view.Content, "\n")
	if len(lines) != h {
		t.Errorf("%s: %d lines, want %d", label, len(lines), h)
	}
	for i, line := range lines {
		if got := ansi.StringWidth(line); got > w {
			t.Errorf("%s: line %d is %d wide", label, i, got)
		}
	}
	if view.Cursor != nil {
		if p := view.Cursor.Position; p.X < 0 || p.X >= w || p.Y < 0 || p.Y >= h {
			t.Errorf("%s: cursor at %v is outside the window", label, p)
		}
	}
	plain := ansi.Strip(view.Content)
	if w < minWidth || h < minHeight {
		joined := strings.Join(strings.Fields(plain), " ")
		if !strings.Contains(joined, views.Enlarge) {
			t.Errorf("%s: no enlarge message:\n%s", label, plain)
		}
		return
	}
	if strings.Contains(plain, views.Enlarge) {
		t.Errorf("%s: enlarge message at a usable size", label)
	}
	if p := m.topPanel(); p != nil {
		if !strings.Contains(plain, "× Close") {
			t.Errorf("%s: a panel with no way out:\n%s", label, plain)
		}
		return
	}
	if m.sideOverlay || m.inspectorTop {
		return
	}
	if m.palette.Open {
		if !strings.Contains(plain, "▸ /") {
			t.Errorf("%s: the palette has no selected row:\n%s", label, plain)
		}
	}
	if !strings.Contains(plain, "+") {
		t.Errorf("%s: no composer:\n%s", label, plain)
	}
	if view.Cursor == nil && m.focus == focusComposer {
		t.Errorf("%s: composer has focus and no cursor", label)
	}
	assertNoOverlap(t, label, m, h)
}

// assertNoOverlap checks that the stacked regions are inside the window and
// disjoint. Frame clipping would otherwise hide a layout that collides.
func assertNoOverlap(t *testing.T, label string, m *Model, h int) {
	t.Helper()
	g := m.geo
	type span struct {
		name string
		y, h int
	}
	spans := []span{{"composer", g.composer.Y, g.composer.H}, {"status", g.status.Y, g.status.H}}
	if m.advanced() {
		spans = append(spans, span{"body", g.body.Y, 0}, span{"footer", g.footer.Y, g.footer.H})
		if g.transcript.Y+g.transcript.H > g.status.Y && g.status.H > 0 {
			t.Errorf("%s: transcript %d+%d runs into status at %d", label, g.transcript.Y, g.transcript.H, g.status.Y)
		}
		if g.body.Y+g.body.H >= g.composer.Y {
			t.Errorf("%s: body %d+%d runs into composer at %d", label, g.body.Y, g.body.H, g.composer.Y)
		}
	} else {
		spans = append(spans, span{"disclaimer", g.disclaimer.Y, g.disclaimer.H}, span{"footer", g.footer.Y, g.footer.H})
		if g.home {
			spans = append(spans, span{"greeting", g.greeting, 1}, span{"cards", g.cards.Y, g.cards.H}, span{"notice", g.appNotice.Y, g.appNotice.H})
		} else {
			spans = append(spans, span{"transcript", g.transcript.Y, g.transcript.H})
			if g.transcript.H < 1 {
				t.Errorf("%s: no room for the conversation", label)
			}
		}
	}
	for i, a := range spans {
		if a.h == 0 {
			continue
		}
		if a.y < 1 || a.y+a.h > h {
			t.Errorf("%s: %s at %d+%d is outside the window", label, a.name, a.y, a.h)
		}
		for _, b := range spans[i+1:] {
			if b.h > 0 && a.y < b.y+b.h && b.y < a.y+a.h {
				t.Errorf("%s: %s at %d+%d overlaps %s at %d+%d", label, a.name, a.y, a.h, b.name, b.y, b.h)
			}
		}
	}
}

func TestFramesFit(t *testing.T) {
	for _, theme := range []string{"dark", "light"} {
		for _, advanced := range []bool{false, true} {
			t.Run(theme+"/"+modeName(advanced), func(t *testing.T) {
				t.Parallel()
				g := newFakeGateway(t)
				d := g.open(t, rig{theme: theme})
				if advanced {
					enterAdvanced(t, d)
				}
				states := 0
				defer func() { t.Logf("%d states at %d sizes", states, len(fitSizes)) }()
				walk(d, g, func(state string) {
					t.Helper()
					states++
					for _, s := range fitSizes {
						d.send(tea.WindowSizeMsg{Width: s[0], Height: s[1]})
						assertFits(t, d, state+" before rewrap", s[0], s[1])
						d.rewrap()
						assertFits(t, d, state, s[0], s[1])
					}
					d.send(tea.WindowSizeMsg{Width: 100, Height: 30})
					d.rewrap()
				})
			})
		}
	}
}

// Text that measures differently from its length must still fit: wide characters,
// combining marks and very long words, in every place gateway text lands.
func TestFramesFitWithDifficultText(t *testing.T) {
	const wide = "全角の文字列と絵文字🙂を含むとても長いタイトルです"
	long := strings.Repeat("unbroken", 30)
	combining := strings.Repeat("é", 40)

	for _, text := range []string{wide, long, combining} {
		g := newFakeGateway(t)
		g.set(func() {
			g.chats[0]["title"] = text
			g.boot["suggestions"] = []map[string]any{{"id": "x", "title": text, "subtitle": text, "action": "prompt", "prompt": "x"}}
			g.boot["application"].(map[string]any)["notice"] = text
			g.script = []string{startOnly("c1", map[string]any{"code": "history_trimmed", "message": text}),
				sse("citations", map[string]any{"citations": []map[string]any{{"number": 1, "title": text, "section": text, "department": text, "version": text}}}),
				sse("status", map[string]any{"phase": "generating", "message": text}),
				sse("delta", map[string]any{"text": text + "\n\n| " + text + " | b |\n|---|---|\n| " + text + " | d |\n\n```\n" + text + "\n```\n"}), doneEvent("stop")}
		})
		for _, advanced := range []bool{false, true} {
			d := g.open(t, rig{})
			if advanced {
				enterAdvanced(t, d)
			}
			check := func(state string) {
				t.Helper()
				for _, s := range fitSizes {
					d.send(tea.WindowSizeMsg{Width: s[0], Height: s[1]})
					d.rewrap()
					assertFits(t, d, state, s[0], s[1])
				}
				d.send(tea.WindowSizeMsg{Width: 100, Height: 30})
				d.rewrap()
			}
			check("home")
			d.submit(text)
			d.finish()
			check("conversation")
		}
	}
}

// One resize produces several messages. Only the last one rewraps, and the frames
// in between still fit.
func TestResizeDebounce(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.reply(g, "Question", richReply)
	width := d.m.transcript.WrapWidth()

	d.send(tea.WindowSizeMsg{Width: 70, Height: 24})
	if got := d.m.transcript.WrapWidth(); got != width {
		t.Fatalf("rewrapped at once, to %d", got)
	}
	assertFits(t, d, "during resize", 70, 24)
	d.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	stale := d.m.resizeGen - 1
	d.send(resizeMsg{gen: stale})
	if got := d.m.transcript.WrapWidth(); got != width {
		t.Fatalf("a stale resize rewrapped, to %d", got)
	}
	d.until("rewrap", func() bool { return d.m.transcript.WrapWidth() != width })
	if got, want := d.m.transcript.WrapWidth(), d.m.geo.transcript.W; got != want {
		t.Errorf("wrapped to %d, column is %d", got, want)
	}
	if !d.m.transcript.AtBottom() {
		t.Error("rewrap lost the bottom of the conversation")
	}
}

// The responsive table in docs/ui-spec.md, row by row.
func TestResponsiveTable(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	size := func(w, h int) {
		d.send(tea.WindowSizeMsg{Width: w, Height: h})
		d.rewrap()
	}
	for _, c := range []struct {
		w          int
		side, cols bool
	}{{120, true, true}, {100, true, true}, {90, true, true}, {89, false, true}, {60, false, true}, {59, false, false}, {40, false, false}} {
		size(c.w, 40)
		if got := d.m.geo.side.W > 0; got != c.side {
			t.Errorf("basic width %d: sidebar %v, want %v", c.w, got, c.side)
		}
		if got := d.m.geo.plan.framed; got != c.cols {
			t.Errorf("basic width %d: framed cards %v, want %v", c.w, got, c.cols)
		}
		if c.w >= 60 {
			d.expect("Digital Commons", "Beta")
		} else {
			d.expect("Beta")
		}
	}

	size(200, 60)
	if w := d.m.geo.col.W; w != columnMax {
		t.Errorf("reading column is %d, want %d", w, columnMax)
	}
	left := d.m.geo.col.X - d.m.geo.side.W
	right := 200 - d.m.geo.col.X - d.m.geo.col.W
	if diff := left - right; diff < -1 || diff > 1 {
		t.Errorf("column off center: %d left, %d right", left, right)
	}

	// Height: the footer and disclaimer go below 20, framed cards below 14.
	size(100, 20)
	d.expect(views.Disclaimer, "Enter to send")
	size(100, 19)
	d.refute(views.Disclaimer, "Enter to send")
	size(100, 14)
	if !d.m.geo.plan.framed {
		t.Errorf("framed cards should fit at 14 rows:\n%s", d.text())
	}
	size(100, 13)
	if d.m.geo.plan.framed {
		t.Error("framed cards at 13 rows")
	}

	enterAdvanced(t, d)
	for _, c := range []struct {
		w          int
		side, insp bool
	}{{120, true, true}, {119, true, false}, {90, true, false}, {89, false, false}, {60, false, false}, {40, false, false}} {
		size(c.w, 40)
		if got := d.m.geo.side.W > 0; got != c.side {
			t.Errorf("advanced width %d: chats %v, want %v", c.w, got, c.side)
		}
		if got := d.m.geo.inspector.W > 0; got != c.insp {
			t.Errorf("advanced width %d: inspector %v, want %v", c.w, got, c.insp)
		}
	}
}

// All seven suggestions must be reachable at every size that shows any.
func TestAllSuggestionsReachable(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	for _, s := range fitSizes {
		if s[0] < minWidth || s[1] < minHeight {
			continue
		}
		d.send(tea.WindowSizeMsg{Width: s[0], Height: s[1]})
		d.rewrap()
		if len(d.m.geo.plan.items) == 0 {
			continue
		}
		seen := map[int]bool{}
		for range 12 {
			for _, i := range d.m.geo.plan.items {
				seen[i] = true
			}
			if !d.m.geo.plan.more {
				break
			}
			d.do("more-cards")
		}
		d.m.cardPage = 0
		if len(seen) != 7 {
			t.Errorf("%dx%d: %d of 7 suggestions reachable", s[0], s[1], len(seen))
		}
	}
}

func TestOutlinedUserBubbleGeometry(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{w: 120, h: 40, profile: colorprofile.ASCII})
	d.reply(g, strings.Repeat("A longer question about the remote work policy. ", 6), "Short answer.")
	d.settle(time.Millisecond)
	r := d.m.geo.transcript
	lines := strings.Split(d.text(), "\n")
	found := 0
	for _, line := range lines[r.Y : r.Y+r.H] {
		start := strings.Index(line, "╭")
		if start < 0 || !strings.Contains(line, "╮") {
			continue
		}
		left := ansi.StringWidth(line[:start])
		right := ansi.StringWidth(strings.TrimRight(line, " "))
		if left < r.X || right > r.X+r.W {
			continue
		}
		found++
		if right != r.X+r.W {
			t.Errorf("bubble ends at %d, column ends at %d", right, r.X+r.W)
		}
		if width := right - left; width*10 > r.W*7 {
			t.Errorf("bubble is %d wide in a column of %d", width, r.W)
		}
	}
	if found == 0 {
		t.Fatalf("no bubble in:\n%s", d.text())
	}
}

func TestUserBubbleIsFlatInColor(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{w: 120, h: 40, profile: colorprofile.TrueColor})
	d.reply(g, strings.Repeat("A longer question about the remote work policy. ", 6), "Short answer.")
	d.settle(time.Millisecond)
	r := d.m.geo.transcript
	lines := strings.Split(d.text(), "\n")
	found := 0
	for _, line := range lines[r.Y : r.Y+r.H] {
		if strings.Contains(line, "╭") {
			t.Errorf("bubble has a border: %q", line)
		}
		start := strings.Index(line, "longer question")
		if start < 0 {
			continue
		}
		found++
		if width := r.X + r.W - ansi.StringWidth(line[:start]); width*10 > r.W*7 {
			t.Errorf("bubble is %d wide in a column of %d", width, r.W)
		}
	}
	if found == 0 {
		t.Fatalf("no bubble in:\n%s", d.text())
	}
}
