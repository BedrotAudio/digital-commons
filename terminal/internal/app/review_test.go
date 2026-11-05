package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"digital-commons/terminal/internal/client"
	"digital-commons/terminal/internal/config"
	"digital-commons/terminal/internal/views"
)

// A fence label the model chooses must not stop the event loop, while the reply
// streams or when the saved chat is opened again.
func TestFenceLabelCannotStopTheApp(t *testing.T) {
	fence := "```jungle\n\"\n```"
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.reply(g, "Question", fence)
	d.expect("jungle")

	g.set(func() {
		g.convs["c1"] = map[string]any{"id": "c1", "title": "Saved", "assistant": "general", "messages": []map[string]any{
			{"id": "m1", "role": "user", "content": "Q", "created_at": "2026-11-02T09:00:00Z", "attachments": []any{}},
			{"id": "m2", "role": "assistant", "content": fence, "created_at": "2026-11-02T09:00:05Z", "attachments": []any{}, "citations": []any{}, "finish_reason": "cancelled"},
		}}
	})
	d.clickText("Remote work rules")
	d.until("the chat to open", func() bool { return d.m.currentID == "c1" })
	d.expect("jungle")
}

func TestDeepQuoteCannotStopTheApp(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.reply(g, "Question", "> "+strings.Repeat(">", 120)+" Still here")
	d.expect("Still here")
}

// A file's name is the person's own, so it is never reworded and never costs them
// the notice. The notice is often the only sign that a file was not read in full.
func TestBasicModeKeepsEveryNoticeAboutAFile(t *testing.T) {
	for _, name := range []string{"API guide.txt", "context.md", "Token report.csv", "GPU budget.csv", "Model_Policy.txt", "my_token.txt", "Server list.csv", "model.txt", "Provider_Directory.csv"} {
		for _, text := range []string{
			"Only part of " + name + " was read: about 12%, 23,877 of 200,000 characters.",
			name + " did not fit and was not read.",
		} {
			t.Run(text, func(t *testing.T) {
				g := newFakeGateway(t)
				d := g.open(t, rig{w: 120, h: 30})
				g.set(func() {
					g.script = []string{startOnly("new1", map[string]any{"code": "attachment_truncated", "message": text}), sse("delta", map[string]any{"text": "Summary."}), doneEvent("stop")}
				})
				d.submit("Summarize")
				d.finish()
				d.expect(text[:min(len(text), 39)]) // the rest wraps
			})
		}
	}
}

// The filter still applies to every other notice.
func TestBasicModeStillFiltersOtherNotices(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{w: 120, h: 30})
	g.set(func() {
		g.script = []string{startOnly("new1", map[string]any{"code": "history_trimmed", "message": "The context token budget ran out."}), sse("delta", map[string]any{"text": "Reply."}), doneEvent("stop")}
	})
	d.submit("Question")
	d.finish()
	d.refute("token budget")
}

// When the provider refuses the prompt, the gateway retries with less and corrects
// what it said in start.
func TestNoticesEventReplacesTheStartNotices(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{w: 120, h: 30})
	g.set(func() {
		g.script = []string{
			startOnly("new1", map[string]any{"code": "attachment_truncated", "message": "Only part of data.csv was read: about 57%."}),
			sse("citations", map[string]any{"citations": []map[string]any{{"number": 1, "title": "One"}, {"number": 2, "title": "Two"}}}),
			sse("notices", map[string]any{"notices": []map[string]any{{"code": "attachment_truncated", "message": "Only part of data.csv was read: about 34%."}}}),
			sse("citations", map[string]any{"citations": []map[string]any{{"number": 1, "title": "One"}}}),
			sse("delta", map[string]any{"text": "Summary."}), doneEvent("stop"),
		}
	})
	d.submit("Summarize")
	d.finish()
	d.expect("about 34%", "[1] One")
	d.refute("about 57%", "[2] Two")
}

// A notice saved with a chat comes back with it.
func TestReopenedChatShowsItsNotices(t *testing.T) {
	g := newFakeGateway(t)
	d := g.open(t, rig{w: 120, h: 30})
	g.set(func() {
		g.convs["c1"] = map[string]any{"id": "c1", "title": "Saved", "assistant": "general", "messages": []map[string]any{
			{"id": "m1", "role": "user", "content": "Summarize", "created_at": "2026-11-02T09:00:00Z", "attachments": []any{}},
			{"id": "m2", "role": "assistant", "content": "A summary.", "created_at": "2026-11-02T09:00:05Z", "attachments": []any{}, "citations": []any{}, "finish_reason": "stop",
				"notices": []map[string]any{{"code": "attachment_truncated", "message": "Only part of handbook.txt was read: about 12%."}}},
		}}
	})
	d.clickText("Remote work rules")
	d.until("the chat to open", func() bool { return d.m.currentID == "c1" })
	d.expect("Only part of handbook.txt was read")
}

func sourcedChat() map[string]any {
	return map[string]any{"id": "c1", "title": "Saved", "assistant": "general", "messages": []map[string]any{
		{"id": "m1", "role": "user", "content": "How many days?", "created_at": "2026-11-02T09:00:00Z", "attachments": []any{}},
		{"id": "m2", "role": "assistant", "content": "Twelve days [1].", "created_at": "2026-11-02T09:00:05Z", "attachments": []any{}, "finish_reason": "stop",
			"citations": []map[string]any{{"number": 1, "title": "Remote Work Policy", "section": "Eligibility"}}},
	}}
}

// Try again on a reopened sourced reply must not quietly replace it with one written
// without the approved documents.
func TestTryAgainOnReopenedSourcedReplyKeepsDocuments(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() {
		g.convs["c1"] = sourcedChat()
		g.script = replyScript(sse("delta", map[string]any{"text": "Twelve days."}), doneEvent("stop"))
	})
	d.clickText("Remote work rules")
	d.until("the chat to open", func() bool { return d.m.currentID == "c1" })
	if d.m.useDocs {
		t.Fatal("opening a chat turned the chip on")
	}
	d.key("ctrl+r")
	d.finish()
	req := g.lastRequest(t)
	if !req.Regenerate || !req.UseDocuments {
		t.Errorf("request %+v", req)
	}
}

// With document search taken away, regenerate must not ask for it and be refused.
func TestTryAgainOnReopenedReplyRespectsThePermission(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() {
		g.boot["permissions"].(map[string]any)["search_documents"] = false
		g.convs["c1"] = sourcedChat()
		g.script = replyScript(sse("delta", map[string]any{"text": "Twelve days."}), doneEvent("stop"))
	})
	d := newDriver(t, g, 100, 30)
	d.clickText("Remote work rules")
	d.until("the chat to open", func() bool { return d.m.currentID == "c1" })
	d.key("ctrl+r")
	d.finish()
	if req := g.lastRequest(t); req.UseDocuments {
		t.Errorf("request %+v", req)
	}
}

// A chat that is still being fetched must not replace the screen once a new turn has
// begun, in either order of arrival.
func TestFetchLandingDuringATurnIsDropped(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	d.reply(g, "First question", "First answer.")
	current := d.m.currentID
	g.set(func() { g.convs["c1"] = sourcedChat() })

	d.m.opening = "c1"
	holdReply(t, d, g, "Second answer.")
	d.send(conversationMsg{id: "c1", conv: &client.Conversation{ConversationSummary: client.ConversationSummary{ID: "c1", Title: "Saved"}}})
	if d.m.currentID != current {
		t.Fatalf("the screen moved to %q", d.m.currentID)
	}
	if d.m.opening != "" {
		t.Errorf("still opening %q", d.m.opening)
	}
	d.expect("First answer.", "A question")
	release(d, g)
}

// When a regenerate writes nothing the gateway keeps the old reply, however the
// stream ended, so the old reply stays on screen.
func TestRegenerateWithNoTextKeepsTheOldReplyOnScreen(t *testing.T) {
	for reason, note := range map[string]string{"length": views.NoRoom, "stop": views.NoReply} {
		t.Run(reason, func(t *testing.T) {
			g := newFakeGateway(t)
			d := newDriver(t, g, 100, 30)
			d.reply(g, "Question", "the good old answer")
			g.set(func() { g.script = replyScript(doneEvent(reason)) })
			d.key("ctrl+r")
			d.finish()
			d.expect("the good old answer", note[:30])
			if got := d.m.transcript.LatestReply(); got != "the good old answer" {
				t.Errorf("latest reply %q", got)
			}
		})
	}
}

// Whatever the person typed while the request was out, the refused text comes back
// and nothing they typed is lost.
func TestRefusedTextSurvivesTypingDuringTheRequest(t *testing.T) {
	g := newFakeGateway(t)
	d := newDriver(t, g, 100, 30)
	g.set(func() {
		g.script = []string{"HOLD", errorEvent("context_too_large", "That message is too long for the assistant. Please shorten it and try again.", false)}
	})
	d.submit("my long carefully written message")
	d.until("the request", func() bool { return d.m.active != nil })
	d.key("and a follow-up")
	release(d, g)

	value := d.m.composer.Value()
	if !strings.Contains(value, "my long carefully written message") || !strings.Contains(value, "and a follow-up") {
		t.Fatalf("composer holds %q", value)
	}
	g.set(func() { g.script = replyScript(sse("delta", map[string]any{"text": "Done."}), doneEvent("stop")) })
	d.key("enter")
	d.finish()
	if !strings.Contains(g.lastRequest(t).Message, "my long carefully written message") {
		t.Errorf("request %+v", g.lastRequest(t))
	}
}

func TestClipboardFailureIsNotReportedAsCopied(t *testing.T) {
	g := newFakeGateway(t)
	clip := &fakeClipboard{writeErr: errFake}
	d := g.open(t, rig{clip: clip})
	d.reply(g, "Question", "Answer.")
	d.key("ctrl+y")
	d.until("the notice", func() bool { return d.m.notice != "" })
	d.refute("Copied.")
}

func TestWhoCanConnectIsCorrectedForAnOrganizationGateway(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() {
		g.privacyBody = map[string]any{
			"locality":   g.boot["locality"],
			"statements": []map[string]any{{"id": "network_exposure", "label": "Who can connect", "value": "Only programs on this computer", "status": "ok", "detail": ""}},
			"notice":     "A notice.",
		}
	})
	d := g.open(t, rig{baseURL: "https://ai.example.gov", w: 120, h: 40})
	d.submit("/privacy")
	d.until("privacy", func() bool { return d.m.privacy != nil && !d.m.topPanel().loading })
	d.refute("on this computer")
	d.refute("● Who can connect")
	d.expect("Who can connect")
}

func TestStatusDoesNotClaimCloudAccessWhenItIsUnknown(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() { g.status["inference"].(map[string]any)["cloud_access_disabled"] = nil })
	d := g.open(t, rig{w: 120, h: 40})
	enterAdvanced(t, d)
	d.submit("/status")
	d.refute("false")
	d.expect("cloud off", "not checked")
	d.closeAll()

	g.set(func() { g.status["inference"].(map[string]any)["cloud_access_disabled"] = false })
	d.send(pollMsg{d.m.pollGen})
	d.until("the new status", func() bool {
		cloud := d.m.status.Inference.CloudAccessDisabled
		return cloud != nil && !*cloud
	})
	d.submit("/status")
	d.expect("false")
}

// Tiers can differ only in settings, so the picker has to show them.
func TestModelPickerShowsTierSettings(t *testing.T) {
	g := newFakeGateway(t)
	g.set(func() {
		g.modelsBody = map[string]any{"provider": "ollama", "tiers": []map[string]any{
			{"tier": "fast", "label": "Fast", "enabled": true, "available": true, "model": "Gemma4:12b", "context_tokens": 8192, "max_output_tokens": 1024, "temperature": 0.2, "thinking": false},
			{"tier": "advanced", "label": "Advanced", "enabled": true, "available": true, "model": "Gemma4:12b", "context_tokens": 8192, "max_output_tokens": 4096, "temperature": 0.7, "thinking": true},
		}}
	})
	d := g.open(t, rig{w: 120, h: 40})
	enterAdvanced(t, d)
	d.submit("/model")
	d.until("the tiers", func() bool { return d.m.models != nil && !d.m.topPanel().loading })
	d.expect("output 1024", "output 4096", "temperature 0.2", "temperature 0.7", "reasoning on", "reasoning off")
}

func TestSelectTextOnEveryMacTerminal(t *testing.T) {
	if got := views.SelectText("darwin", "vscode"); !strings.Contains(got, "Mac Option Click Forces Selection") {
		t.Errorf("VS Code: %q", got)
	}
	if got := views.SelectText("darwin", "iTerm.app"); !strings.Contains(got, "hold Option and drag") {
		t.Errorf("iTerm: %q", got)
	}
	if got := views.SelectText("darwin", "Apple_Terminal"); !strings.Contains(got, "hold Fn and drag") {
		t.Errorf("Terminal: %q", got)
	}
	if got := views.SelectText("windows", "vscode"); !strings.Contains(got, "hold Shift and drag") {
		t.Errorf("Windows: %q", got)
	}
}

func TestChangingOneChoiceLeavesTheOtherToTheConfigFile(t *testing.T) {
	g := newFakeGateway(t)
	path := filepath.Join(t.TempDir(), "state.json")
	d := g.open(t, rig{config: func(c *config.Config) { c.StatePath = path }})

	d.submit("/theme light")
	d.until("save", func() bool { return !d.m.saving })
	state := readState(t, path)
	if _, pinned := state["mouse"]; pinned || state["theme"] != "light" {
		t.Errorf("after a theme change: %v", state)
	}

	d.do2("set-mouse", "off")
	d.until("save", func() bool { return !d.m.saving })
	state = readState(t, path)
	if state["mouse"] != false || state["theme"] != "light" {
		t.Errorf("after a mouse change: %v", state)
	}
}

// Two changes made while a save is running both reach the file.
func TestChoicesMadeDuringASaveAreKept(t *testing.T) {
	g := newFakeGateway(t)
	path := filepath.Join(t.TempDir(), "state.json")
	d := g.open(t, rig{config: func(c *config.Config) { c.StatePath = path }})
	d.do2("set-theme", "light")
	d.do2("set-mouse", "off")
	d.until("save", func() bool {
		state := readState(t, path)
		return !d.m.saving && state["mouse"] == false && state["theme"] == "light"
	})
}

func readState(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}
	}
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

// A Mac terminal escapes a dragged path with backslashes.
func TestDraggedPathsAttach(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "Remote Work.txt"), []byte("Body"), 0o600); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(home, "Remote Work.txt")
	for name, c := range map[string]struct{ platform, typed string }{
		"mac drag":      {"darwin", strings.ReplaceAll(filepath.ToSlash(full), " ", `\ `)},
		"quoted":        {"windows", `"` + full + `"`},
		"tilde":         {"darwin", "~/Remote Work.txt"},
		"tilde windows": {"windows", `~\Remote Work.txt`},
	} {
		t.Run(name, func(t *testing.T) {
			if c.platform == "windows" && runtime.GOOS != "windows" {
				t.Skip("a backslash is part of a file name outside Windows")
			}
			if got := attachmentPath(c.typed, c.platform, home); filepath.Clean(got) != filepath.Clean(full) {
				t.Errorf("%q became %q", c.typed, got)
			}
		})
	}
	if got := attachmentPath(`C:\Users\pat\a.txt`, "windows", home); got != `C:\Users\pat\a.txt` {
		t.Errorf("a Windows path changed: %q", got)
	}
	if got := attachmentPath(`'/tmp/odd\name.txt'`, "darwin", home); got != `/tmp/odd\name.txt` {
		t.Errorf("a quoted path changed: %q", got)
	}
}

func TestDraggedPathAttachesThroughTheCommand(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "Remote Work.txt"), []byte("Body"), 0o600); err != nil {
		t.Fatal(err)
	}
	g := newFakeGateway(t)
	d := g.open(t, rig{platform: "darwin", home: home, w: 120, h: 30})
	d.submit(`/attach ~/Remote\ Work.txt`)
	d.until("the file", func() bool { return len(d.m.attachments) == 1 })
}
