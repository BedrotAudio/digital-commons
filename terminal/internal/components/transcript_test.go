package components

import (
	"slices"
	"strings"
	"testing"
	"time"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/theme"
	"digital-commons/terminal/internal/views"

	"github.com/charmbracelet/x/ansi"
)

func newTranscript() *Transcript {
	return NewTranscript(theme.New(theme.NameDark), time.Now)
}

// A finished message is rendered once for an id, width and theme.
func TestFinishedMessagesAreCached(t *testing.T) {
	tr := newTranscript()
	tr.Wrap(60)
	msg := tr.Add(&Message{Role: "assistant", Text: "Some **text**."})
	tr.Rebuild()
	if len(tr.cache) != 1 {
		t.Fatalf("%d cached blocks", len(tr.cache))
	}
	first := tr.block(msg, 1)
	tr.Rebuild()
	if tr.block(msg, 1) != first {
		t.Error("a finished message was rendered again")
	}

	tr.Wrap(50)
	tr.Rebuild()
	if len(tr.cache) != 2 {
		t.Error("a new width did not render")
	}
	tr.SetTheme(theme.New(theme.NameLight))
	tr.Rebuild()
	if len(tr.cache) != 3 {
		t.Error("a new theme did not render")
	}
	msg.Text = "Changed."
	msg.Touch()
	tr.Rebuild()
	if len(tr.cache) != 4 {
		t.Error("a changed message did not render")
	}
}

// Only the streaming message costs anything, and it is never cached.
func TestStreamingMessagesAreNotCached(t *testing.T) {
	tr := newTranscript()
	tr.Wrap(60)
	tr.Add(&Message{Role: "user", Text: "Hello"})
	reply := tr.Add(&Message{Role: "assistant", Text: "Partial", Streaming: true})
	tr.Rebuild()
	if len(tr.cache) != 1 {
		t.Fatalf("%d cached blocks", len(tr.cache))
	}
	reply.Text = "Partial and more"
	tr.Rebuild()
	if !strings.Contains(ansi.Strip(tr.View()), "and more") {
		t.Errorf("stale view:\n%s", ansi.Strip(tr.View()))
	}
}

// Code numbers run across messages, and a message's blocks move with its neighbors.
func TestCodeNumbersRunAcrossMessages(t *testing.T) {
	tr := newTranscript()
	tr.Wrap(60)
	tr.Add(&Message{Role: "assistant", Text: "```go\none\n```"})
	tr.Add(&Message{Role: "assistant", Text: "```go\ntwo\n```\n\n```go\nthree\n```"})
	tr.Rebuild()
	blocks := tr.CodeBlocks()
	if len(blocks) != 3 {
		t.Fatalf("%d blocks", len(blocks))
	}
	for i, want := range []string{"one", "two", "three"} {
		if blocks[i].Number != i+1 || blocks[i].Text != want {
			t.Errorf("block %d: %+v", i, blocks[i])
		}
	}
	tr.Messages = tr.Messages[1:]
	tr.Rebuild()
	if blocks := tr.CodeBlocks(); blocks[0].Number != 1 || blocks[0].Text != "two" {
		t.Errorf("renumbered: %+v", blocks)
	}
}

func citation(title, section, department, version, date string, page *int) client.Citation {
	return client.Citation{Title: title, Section: section, Department: department, Version: version, DocumentDate: date, Page: page}
}

func TestCitationLines(t *testing.T) {
	page := 4
	got := citationText(citation("Policy", "Eligibility", "HR", "2.1", "2026-03-01", &page))
	if want := "Policy, Eligibility · HR · v2.1, 2026-03-01, page 4"; got != want {
		t.Errorf("got %q", got)
	}
	if got := citationText(citation("Policy", "", "", "", "", nil)); got != "Policy" {
		t.Errorf("got %q", got)
	}
}

func TestWaitLineUsesPlainWordingInBasic(t *testing.T) {
	tr := newTranscript()
	tr.Wrap(60)
	m := &Message{Role: "assistant", Streaming: true, Status: "Loading the model"}
	if line := ansi.Strip(tr.waitLine(m, 50)); !strings.Contains(line, "Loading the assistant") {
		t.Errorf("Basic wait line: %q", line)
	}
	tr.Advanced = true
	if line := ansi.Strip(tr.waitLine(m, 50)); !strings.Contains(line, "Loading the model") {
		t.Errorf("Advanced wait line: %q", line)
	}
}

func TestChipsTruncateAndStayClickable(t *testing.T) {
	c := NewComposer(theme.New(theme.NameDark))
	c.SetWidth(50)
	c.Chips = []Chip{{Label: strings.Repeat("long name ", 8), Action: views.Action{Kind: "remove-file"}}, {Label: "b.txt", Action: views.Action{Kind: "remove-file", N: 1}}}
	out := c.Render(true)
	for i, line := range strings.Split(ansi.Strip(out.Content), "\n") {
		if w := ansi.StringWidth(line); w != 50 {
			t.Errorf("line %d is %d wide: %q", i, w, line)
		}
	}
	found := 0
	for _, z := range out.Zones {
		if z.Action.Kind == "remove-file" {
			found++
		}
	}
	if found != 2 {
		t.Errorf("%d chip targets", found)
	}
}

func TestWrapKeepsDatesAndCutsLongWords(t *testing.T) {
	lines := views.Wrap("Issued 2026-03-01 by the Human Resources department", 20)
	joined := strings.Join(lines, " ")
	if !strings.Contains(joined, "2026-03-01") {
		t.Errorf("split a date: %q", lines)
	}
	for _, line := range views.Wrap(strings.Repeat("x", 50), 20) {
		if w := ansi.StringWidth(line); w > 20 {
			t.Errorf("line of %d", w)
		}
	}
	for _, line := range views.Wrap("全角の文字列を含むとても長い行です", 5) {
		if w := ansi.StringWidth(line); w > 5 {
			t.Errorf("wide line of %d", w)
		}
	}
}

func TestIsCommandLine(t *testing.T) {
	cases := map[string]bool{
		"/":                     true,
		"/help":                 true,
		"/model fast":           true,
		"/attach /tmp/notes.md": true,
		"/Users/a/notes.txt":    false,
		"/usr/bin/env python":   false,
		"//comment":             false,
		"hello":                 false,
		"/help\nmore":           false,
	}
	for text, want := range cases {
		if got := IsCommandLine(text); got != want {
			t.Errorf("%q: got %v", text, got)
		}
	}
}

// Sources lists what the reply cites, with the numbers as sent. The reply narrows
// the list and never adds to it, and a reply that cites nothing keeps every source.
func TestSourcesAreTheOnesTheReplyCites(t *testing.T) {
	sent := []client.Citation{{Number: 1, Title: "One"}, {Number: 2, Title: "Two"}, {Number: 3, Title: "Three"}, {Number: 4, Withheld: true}}
	numbers := func(cs []client.Citation) []int {
		var out []int
		for _, c := range cs {
			out = append(out, c.Number)
		}
		return out
	}
	for text, want := range map[string][]int{
		"Twelve days [1].":                         {1},
		"See [3], then [1] and [3] again.":         {1, 3},
		"Both say so [1][3].":                      {1, 3},
		"Both say so [1, 3].":                      {1, 3},
		"Withheld [4].":                            {4},
		"No citation at all.":                      {1, 2, 3, 4},
		"Only a source that was never sent [9].":   {1, 2, 3, 4},
		"A sent one and one that was not [2] [9].": {2},
		"[01] and [ 3 ] and [x]":                   {1},
		"":                                         {1, 2, 3, 4},
	} {
		if got := numbers(citedSources(sent, text)); !slices.Equal(got, want) {
			t.Errorf("%q: got %v, want %v", text, got, want)
		}
	}
}

func TestSourcesBlockShowsOnlyCitedSources(t *testing.T) {
	tr := newTranscript()
	tr.Wrap(80)
	tr.Add(&Message{Role: "assistant", Text: "Twelve days [2].", Citations: []client.Citation{{Number: 1, Title: "Unrelated"}, {Number: 2, Title: "Remote Work Policy"}}})
	tr.Rebuild()
	view := ansi.Strip(tr.View())
	if !strings.Contains(view, "[2] Remote Work Policy") || strings.Contains(view, "Unrelated") {
		t.Errorf("sources:\n%s", view)
	}
}

// A source from the invented sample collection says so wherever it is listed.
func TestSampleSourcesAreLabelled(t *testing.T) {
	sample := citationText(client.Citation{Title: "Procurement Thresholds", Section: "Thresholds", Department: "Finance", Sample: true})
	if !strings.Contains(sample, "invented sample") {
		t.Errorf("sample source: %q", sample)
	}
	if real := citationText(client.Citation{Title: "Policy", Section: "Scope"}); strings.Contains(real, "sample") {
		t.Errorf("real source: %q", real)
	}
}
