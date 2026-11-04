package views

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	disclaimerA = "Digital Commons can make mistakes."
	disclaimerB = "Check important information."
)

// DisclaimerLines breaks at the sentence when one line is too wide.
func DisclaimerLines(width int) []string {
	if width >= len(Disclaimer) {
		return []string{Disclaimer}
	}
	if width >= len(disclaimerA) {
		return []string{disclaimerA, disclaimerB}
	}
	return Wrap(Disclaimer, width)
}

// Fixed wording. Basic mode text lives here, so that the vocabulary test has one
// place to look.
const (
	Greeting      = "What can I help you with?"
	Placeholder   = "Message Digital Commons"
	Disclaimer    = disclaimerA + " " + disclaimerB
	Thinking      = "Thinking…"
	Connecting    = "Connecting…"
	Enlarge       = "Please enlarge this window"
	NotRunning    = "Digital Commons isn't running on this computer yet."
	Unreachable   = "The service can't be reached. Please contact your IT team."
	NoAccess      = "You don't have access to this service. Please contact your IT team."
	GettingReady  = "Getting ready. The first response can take a few minutes."
	NotResponding = "The AI service isn't responding."
	NotSetUp      = "The assistant isn't set up yet. Please contact your IT team."
	Lost          = "The connection was lost."
	Stopped       = "Stopped"
	CutShort      = "This reply was cut short."
	Interrupted   = "This reply was interrupted."
	NoRoom        = "The assistant ran out of room before answering. Try again, or ask a shorter question."
	NoReplySaved  = "No reply was saved."
	NoReply       = "The assistant didn't write anything. Please try again."
	GenericError  = "Something went wrong. Please try again."
	OrgServer     = "Organization server"
	NotConnected  = "Not connected"
	DemoMode      = "Demo mode"
	// CopyUnconfirmed is for a copy handed to the terminal, which never says if it worked.
	CopyUnconfirmed = "Copy sent to this window. If it won't paste, select the text with the mouse."
)

// bannedPattern is the Vocabulary section of docs/ui-spec.md, plus the words of
// its third principle.
var bannedPattern = regexp.MustCompile(`(?i)(?:\b|_)(?:models?|llms?|inference|gateways?|backends?|servers?|ollama|tokens?|contexts?|prompts?|tiers?|providers?|gpus?|apis?)(?:\b|_)`)

// BannedWords returns every technical word in s. "Organization server" is the
// indicator text the spec fixes, so it does not count.
func BannedWords(s string) []string {
	return bannedPattern.FindAllString(strings.ReplaceAll(s, OrgServer, ""), -1)
}

var plainWords = map[string]string{
	"model": "assistant", "models": "assistant", "llm": "assistant", "llms": "assistant",
	"inference": "assistant", "gateway": "service", "gateways": "service",
	"backend": "service", "backends": "service", "server": "service", "servers": "service",
	"ollama": "AI service", "provider": "service", "providers": "service",
	"tier": "option", "tiers": "options", "prompt": "message", "prompts": "messages",
}

// Plain rewrites gateway text for Basic mode. The gateway's messages are meant to
// be plain, but this mode must hold even when one is not. The second result is
// false when the text still has a technical word and must not be shown.
func Plain(s string) (string, bool) {
	out := bannedPattern.ReplaceAllStringFunc(strings.ReplaceAll(s, OrgServer, "\x00"), func(word string) string {
		plain, ok := plainWords[strings.ToLower(word)]
		if !ok {
			return word
		}
		if word[0] >= 'A' && word[0] <= 'Z' {
			return strings.ToUpper(plain[:1]) + plain[1:]
		}
		return plain
	})
	out = strings.ReplaceAll(out, "\x00", OrgServer)
	return out, len(BannedWords(out)) == 0
}

// PlainOr is Plain with a fallback for text that cannot be made plain.
func PlainOr(s, fallback string) string {
	if out, ok := Plain(s); ok {
		return out
	}
	return fallback
}

// PlainError returns the Basic mode wording for a gateway error.
func PlainError(code, message string) string {
	switch code {
	case "model_not_installed", "model_not_permitted":
		return NotSetUp
	case "model_load_failed":
		return "The assistant couldn't get ready. Please try again."
	}
	return PlainOr(message, GenericError)
}

// PythonCommand names Python the way this platform does.
func PythonCommand(goos string) string {
	if goos == "windows" {
		return "python"
	}
	return "python3"
}

// HowToStart tells a person on this computer how to start the service.
func HowToStart(goos string) string {
	window := "a command window"
	if goos == "darwin" {
		window = "Terminal"
	}
	return "To start it, open " + window + " in the Digital Commons folder and run: " + PythonCommand(goos) + " dc.py dev"
}

// CloudAccessOn is the Advanced wording for a refused Ollama cloud setting.
func CloudAccessOn(goos string) string {
	return "Ollama cloud access is on. Run `" + PythonCommand(goos) + " dc.py setup`."
}

// SelectText says how to select text with the mouse in this terminal, because mouse
// capture takes the plain drag.
func SelectText(goos, termProgram string) string {
	modifier := "Shift"
	if goos == "darwin" {
		switch termProgram {
		case "iTerm.app":
			modifier = "Option"
		case "Apple_Terminal":
			modifier = "Fn"
		case "vscode":
			// Off by default. Without it VS Code passes the drag to this program.
			return "The mouse clicks buttons here. To select text with the mouse, turn on Mac Option Click Forces Selection in VS Code's settings, then hold Option and drag."
		}
	}
	return "The mouse clicks buttons here. To select text with the mouse, hold " + modifier + " and drag."
}

// FolderRefused is the message for a folder that cannot be listed. macOS can keep a
// program out of Desktop, Documents and Downloads until the person allows it.
func FolderRefused(goos string, denied bool) string {
	if goos == "darwin" && denied {
		return "macOS is keeping this folder private. To allow it, open System Settings, Privacy & Security, Files and Folders."
	}
	return "That folder can't be opened."
}

// Elapsed formats a wait in calm, readable units.
func Elapsed(seconds int) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
}
