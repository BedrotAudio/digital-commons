# Digital Commons Terminal UI Specification

The most important quality of this product is that a public employee who has
never opened a terminal can use it, and that anyone who has used ChatGPT feels
at home. Advanced mode is the same product with the covers off.

## Principles

1. **Basic mode is ChatGPT in a terminal.** Sidebar of past chats, centered home
   screen, composer at the bottom, Enter to send, streaming reply, stop button.
2. **Nothing requires memorising.** Every action is visible, labelled and
   clickable. Keyboard shortcuts speed things up but are never the only way.
3. **Plain language.** Basic mode never says model, tier, token, inference,
   gateway, backend, provider, Ollama, GPU, context or API. See "Vocabulary".
4. **Restraint.** Government enterprise, not hacker or gaming. No neon, no
   gradients, no ASCII-art logo, no emoji.
5. **Never lie about privacy.** The indicator reflects what the gateway reports.

## Themes

Two themes, chosen by `appearance.theme`: `auto` (default), `dark` or `light`.
`auto` asks the terminal for its background and falls back to `dark`. The UI
paints every cell, so a chosen theme looks right regardless of the terminal's
own colors. Colors are tokens in one file. Nothing else names a color.

| Token | Dark | Light | Use |
|---|---|---|---|
| `bg` | `#0F1720` | `#F7F8F6` | Main canvas |
| `surface` | `#151F29` | `#EEF1EE` | Sidebar, panels |
| `surfaceRaised` | `#1D2935` | `#E3E8E4` | User bubble, focused card, selected rows |
| `border` | `#2E3C4A` | `#C5CEC8` | Separators, inactive borders |
| `text` | `#E6EBF0` | `#1B2730` | Primary text |
| `textMuted` | `#9DABB8` | `#4F5E6A` | Secondary text |
| `accent` | `#74B892` | `#2B6A4C` | Brand, focus, primary actions |
| `accentText` | `#0F1720` | `#FFFFFF` | Text on an accent fill |
| `info` | `#8FB3D9` | `#2A5C8A` | Links, citations |
| `warn` | `#D9B36C` | `#7D5410` | Warnings, demo mode |
| `danger` | `#E58F8F` | `#A23535` | Errors, destructive actions |

`text`, `textMuted`, `accent`, `info`, `warn` and `danger` must reach WCAG AA
(4.5:1) on `bg`, `surface` and `surfaceRaised`, and `accentText` on `accent`. A
test enforces this. `NO_COLOR` is honored: layout, labels and symbols must still
carry all meaning, so state is never shown by color alone.

Glyphs are limited to widely supported single-width characters: `●` `○` `◆` `▸`
`▾` `│` `─` `╭` `╮` `╰` `╯` `■` `+` `×` `·`. Unicode width is measured, never
assumed.

## Basic mode

### Home screen

```
╭──────────────────────╮  ● On this computer                    Digital Commons  Beta
│ ◆ Digital Commons    │
│                      │
│ + New chat           │
│                      │
│ Today                │               What can I help you with?
│ ▸ Remote work rules  │
│   Budget memo draft  │     ╭──────────────────────────╮ ╭──────────────────────────╮
│                      │     │ Summarize a document     │ │ Compare two policies     │
│ Previous 7 days      │     │ Get the key points       │ │ See what changed         │
│   Vendor letter      │     ╰──────────────────────────╯ ╰──────────────────────────╯
│                      │     ╭──────────────────────────╮ ╭──────────────────────────╮
│                      │     │ Draft correspondence     │ │ Explain a regulation     │
│                      │     │ Letters, emails, memos   │ │ In plain language        │
│                      │     ╰──────────────────────────╯ ╰──────────────────────────╯
│                      │
│                      │     ╭────────────────────────────────────────────────────╮
│ Privacy              │     │ + Message Digital Commons                          │
│ Settings             │     ╰────────────────────────────────────────────────────╯
│ Help                 │       Digital Commons can make mistakes. Check important information.
╰──────────────────────╯
```

The sketch shows structure, not exact characters. The sidebar uses a `surface`
fill with no outer border. Show as many suggestion cards as fit; on narrow or
short screens drop to one column, then to single-line rows, then to none. All
seven suggestions from the brief should be reachable.

### Conversation

```
                                             ╭────────────────────────────╮
                                             │ What is our remote work    │
                                             │ eligibility policy?        │
                                             ╰────────────────────────────╯

  ◆ Employees may work remotely after completing their
    probationary period, with supervisor approval [1].

    Sources
    [1] Remote Work Policy, Eligibility · Human Resources · v2.1, 2026-03-01
        · invented sample, not a real policy

    Copy · Try again
```

- Reading column is centered, at most 78 cells wide, narrower on small screens.
- User messages are right-aligned bubbles on `surfaceRaised`, at most 70% of the
  column, as a flat fill with no border. Where there is no color to fill them, they
  are outlined instead. Assistant messages are left-aligned with no bubble, led by
  an `accent` `◆`.
- Suggestion cards are outlines with no fill, because a fill cannot meet a border
  drawn in the middle of a cell. The focused card gets an `accent` border and a
  `surfaceRaised` inside.
- Text is rendered as Markdown. Headings stay modest. Tables and lists wrap.
- Links render as `text (full url)` and are never hidden behind their text.
- While waiting: an animated dot, then `status.message` verbatim, then elapsed time
  once past 10 seconds. With no status it says "Thinking…". On this hardware a
  reply can take minutes, so the wait must always look alive and honest.
- While streaming, a thin caret marks the end of the text, and the composer
  shows `■ Stop` with its shortcut.
- Under the latest finished reply: `Copy · Try again`, clickable.
- Failures appear inline as a calm notice, with `Try again` when it would help,
  never a stack trace. The user's text and files are kept.
- A stopped reply shows "Stopped" beneath the kept text. A lost connection keeps the
  partial text and shows "The connection was lost."
- `notices` from `start` appear as a quiet line above the reply. A later `notices`
  event replaces them, and a later `citations` event replaces the Sources. A reopened
  chat shows the notices stored with each reply.
- Sources appear once the reply has finished, and list only the sources its text
  cites as `[n]` (several at once as `[1, 2]`), with their numbers as sent, so a
  passage retrieval returned but the reply never used is not shown. A reply that
  cites none of them lists them all, so a source is never lost. The list comes only
  from the `citations` event: the reply's text can narrow it and never add to it.
  A source with `sample: true` ends with "invented sample, not a real policy".

### Composer

- Rounded border. `accent` when focused, `border` otherwise.
- Starts at one line, grows to six, then scrolls internally.
- Placeholder "Message Digital Commons". A `+` at the left opens the file picker.
- Enter sends. Ctrl+J adds a line everywhere. Shift+Enter adds a line wherever the
  terminal reports it, which the app must request itself on Windows. It does so in
  Windows Terminal and a bare console window, and leaves other terminals on plain
  keys, because their arrow keys would otherwise arrive as stray text. The footer
  shows Shift+Enter only after the terminal confirms support, and always shows
  Ctrl+J otherwise, so a hint is never false.
- Typing stays enabled during a reply, but Enter does not send until it ends.
- Pasted multi-line text must stay in the composer, not send on each newline.
- Check `limits.max_message_chars` at send, with a plain-language notice. Never
  truncate silently.
- Attached files appear as removable chips above the text:
  `policy.txt ×`. The approved-document mode appears the same way:
  `Approved documents ×`.
- A message may be empty when a file is attached.
- On the home screen the composer sits near the middle, under the suggestions.
  After the first message it moves to the bottom.

### Sidebar

- `+ New chat`, then conversations grouped as Today, Yesterday, Previous 7 days,
  Older. The current conversation is highlighted. Long titles truncate.
- Bottom items: Privacy, Settings, Help.
- Delete is available on the selected row and always asks for confirmation.
- Collapses automatically on narrow terminals and opens as an overlay with
  Ctrl+B.

### Header

One line. Left: the locality indicator. Right: `Digital Commons` and a `Beta`
label. The indicator is redrawn from every status poll.

| State | Basic | Advanced |
|---|---|---|
| `this_device` | `● On this computer` | `● On this computer` |
| `organization` | `● Organization server` | `● Organization server` |
| `demo` | `● Demo mode`, in `warn` | `● Demo mode`, in `warn` |
| Disconnected | `○ Not connected`, in `danger` | `○ Not connected`, in `danger` |

The label comes from `locality.label`, except that `demo` always reads `Demo mode`.
The word stays when disconnected, so meaning survives without color. Never add "Private" or "Secure" to this text, because no
check backs those words. If `gateway.base_url` is not loopback, any `this_device`
claim is ignored and fixed organization text is shown instead, in the indicator
and in the privacy panel.

`application.notice` appears on the home screen under the composer and in Help.

## Advanced mode

Same brand, tokens and composer behaviour. Denser, left-aligned and more
informative. Conversation column uses the full width.

```
◆ Digital Commons  ADVANCED  ● On this computer  gateway ●  auto → standard  Gemma4:12b  CODE
┌ chats ──────┬ conversation ────────────────────────────────┬ inspector ─────────┐
│ + new       │ you · 15:04                                  │ MODEL              │
│ ▸ Router    │ Refactor this handler.                       │ route  auto→standard│
│   Sync bug  │                                              │ model  Gemma4:12b  │
│             │ assistant · standard · 6.4 tok/s · 388 tok   │ ctx    8192        │
│             │ Here is a smaller version:                   │ reason code markers│
│             │ ╭─ [1] go ───────────────────────────╮       │ USAGE              │
│             │ │ func handle() { ... }              │       │ prompt 152 tok     │
│             │ ╰────────────────────────────────────╯       │ output 388 tok     │
│             │                                              │ speed  6.4 tok/s   │
│             │                                              │ first  2.2 s       │
│             │                                              │ total  61 s        │
│             │                                              │ CONTEXT            │
│             │                                              │ ▓▓▓░░░░░ 1.2k/6.1k │
│             │                                              │ BACKEND            │
│             │                                              │ gateway ● 4 ms     │
│             │                                              │ ollama  ● 0.40.13  │
├─────────────┴──────────────────────────────────────────────┴────────────────────┤
│ > /model standard                                                               │
└─────────────────────────────────────────────────────────────────────────────────┘
 /help · /model · /code · /status · F3 inspector            ollama ● idle · 6.4 tok/s
```

- Three panes on wide terminals. On medium widths the inspector is hidden and F3
  opens it as an overlay. On narrow widths the chat list hides too.
- Code blocks are syntax highlighted, carry a number and language, and can be
  copied with `/copy code 1`, by clicking `[1]`, or Ctrl+Y for the latest block.
- `/code` toggles the coding assistant. A `CODE` label shows while it is active.
- Reasoning, when a tier has it on, streams as dimmed collapsible text in Advanced
  only. It is held in memory and is not stored, so reopened chats do not have it.
  Basic mode never renders it.
- Diagnostics respect `view_diagnostics` and remain within the gateway's rules.
- Context controls are `/clear` and the context meter. There are no others.

## Commands

Typing `/` as the first character opens a filtered list above the composer. Up
and Down move, Tab completes, Enter runs, Esc closes. Commands available in the
current mode only are listed.

| Command | Basic | Advanced | Action |
|---|---|---|---|
| `/help` | yes | yes | Help panel |
| `/new` | yes | yes | New chat |
| `/clear` | yes | yes | New chat. The old one stays in history. |
| `/attach [path]` | yes | yes | Attach a file, or open the picker |
| `/search [question]` | yes | yes | Ask approved documents. No question toggles the mode. |
| `/privacy` | yes | yes | Privacy panel |
| `/status` | yes | yes | Friendly status in Basic, full diagnostics in Advanced |
| `/copy` | yes | yes | Copy the latest reply |
| `/theme auto\|dark\|light` | yes | yes | Change theme |
| `/mode basic` | yes | yes | Switch to Basic |
| `/mode developer` | yes | yes | Switch to Advanced. `/mode advanced` also works. |
| `/delete` | yes | yes | Delete this chat, after confirmation |
| `/exit` | yes | yes | Quit |
| `/model auto\|fast\|standard\|advanced` | no | yes | Choose a tier. No argument opens a picker. |
| `/code` | no | yes | Toggle coding assistant |
| `/copy code [n]` | no | yes | Copy a code block |

In Basic mode an Advanced command replies with a short explanation rather than
failing silently. `/mode developer` is refused with a clear message when the
gateway does not grant `advanced_mode`.

Mode values on the wire and in config are `basic` and `developer`. A configured
`developer` default without `advanced_mode` starts in Basic with a short notice.
Mode is not persisted, so Basic stays the default. Basic always sends `tier: auto`
and `assistant: general`. Advanced choices stay in memory for the session and return
when switching back. `assistant` is restored from the conversation on open in
Advanced.

## During an active reply

| Action | Result |
|---|---|
| Typing | Stays enabled. Enter does not send until the reply ends. |
| Panels, `/theme`, `/mode` | Work. The stream continues. |
| `/model`, `/code` | Apply to the next turn. |
| Open another chat, New chat, `/clear` | Stop the reply, keep the partial text, then navigate. |
| Delete current, delete all | Confirm, then stop, then delete, then show home. |
| Ctrl+R | Ignored. |
| `/exit`, Ctrl+C twice | Stop, then quit. |
| Esc | First closes a list, then a panel, then stops the reply. |

`/delete` with no current chat says there is nothing to delete. After any delete the
app shows home. A `busy` or `conversation_locality_changed` reply appears inline; the
second offers New chat.

## Keys

| Key | Action |
|---|---|
| Enter | Send, or activate the focused item |
| Ctrl+J, or Shift+Enter where supported | New line |
| Ctrl+V, or Cmd+V on a Mac | Paste |
| Esc | Close a list, then a panel, then stop the reply |
| Ctrl+N | New chat |
| Ctrl+B | Toggle sidebar |
| Delete | Delete the highlighted chat in the list, after confirmation |
| Backspace | In an empty composer, remove the last chip |
| Ctrl+O | Attach a file |
| Ctrl+Y | Copy the latest reply, or the latest code block in Advanced |
| Ctrl+R | Try again |
| Ctrl+T | Cycle theme |
| Tab, Shift+Tab | Move focus between composer, chat list and conversation |
| PgUp, PgDn, wheel | Scroll the conversation |
| F1 | Help |
| F2 | Settings |
| F3 | Inspector, Advanced only |
| Ctrl+C | Stop a reply. When idle, press twice to quit. |

Alt+Enter is deliberately unused because Windows Terminal reserves it. The footer
must tell the truth about what works.

### On a Mac

- Most Mac laptops need Fn for F1 to F3, so footer hints name `/help` rather than a
  function key on macOS. The F keys still work.
- Terminal.app keeps Page Up and Page Down for itself. When focus is on the
  conversation, Up, Down, Home and End scroll it, on both platforms.
- Terminal.app does not report Shift+Enter, so Ctrl+J stays the hint there.
- Win32-input mode is Windows only and is never requested elsewhere.
- Key names stay `Ctrl`, which is the Control key on a Mac keyboard.
- Messages that name a command use `python` on Windows and `python3` everywhere else.

## Mouse

On by default, `appearance.mouse: false` disables it. Clickable: sidebar rows and
items, suggestion cards, the `+`, attachment chips, `Copy`, `Try again`, `■ Stop`,
panel controls. The wheel scrolls whatever is under the pointer. Mouse capture
means terminals need a modifier to select text: Shift+drag on Windows and in most
terminals, Option+drag in iTerm2, Fn+drag in Terminal.app. VS Code on a Mac selects
with Option+drag only once its Mac Option Click Forces Selection setting is on, which
is off by default, so Help says to turn it on. Help shows the one for this terminal,
using `TERM_PROGRAM` on macOS.

## Panels

Modal, centered, on `surface`, with a title and an obvious way out. Esc always
closes. Narrow terminals use the full width.

- **Help**: what the app does, the keys, how to attach a file, mode switching, the
  beta notice, and how to select text with the mouse in this terminal.
- **Privacy**: `label`, `value` and `status` from `/v1/privacy`, plus the delete-all
  control. `detail` shows in Advanced only.
- **Settings**: appearance, mode, delete all conversations.
- **Status**: friendly in Basic, full in Advanced.
- **Model picker**: Advanced only, listing tiers and their availability, with each
  tier's model, context, output limit, temperature and whether reasoning is on,
  because tiers can differ only in those settings.
- **File picker**: a small purpose-built panel, keyboard and mouse navigable. Starts
  in the home folder, has a drive row on Windows (the letters Windows reports in use,
  found without opening any drive) and a row for `/Volumes` on macOS,
  and filters case-insensitively to `limits.attachment_extensions`. It shows the
  accepted types and the line "Save PDF or Word files as text first." `/attach` on
  another type refuses with that sentence. macOS can refuse Desktop, Documents and
  Downloads until the person allows it, so a refused folder shows a plain message
  that names System Settings, Privacy & Security, Files and Folders.
- **Confirm**: for destructive actions. Default focus is Cancel.

Files are decoded by the terminal: UTF-8 with any BOM stripped, UTF-16 with a BOM,
otherwise Windows-1252, which is what Excel writes for CSV.

## Color depth

Terminal.app before macOS 26 has 256 colors and no true color. Every frame must stay
fully painted and legible at 256 colors in both themes, and a test checks text
contrast after colors are reduced to that palette, against a floor of 3:1 because the
palette has no exact match for most tokens.

## Responsive behaviour

| Width | Basic | Advanced |
|---|---|---|
| 120+ | Sidebar and centered column | Chats, conversation, inspector |
| 90 to 119 | Sidebar and centered column | Chats and conversation |
| 60 to 89 | Column only, sidebar overlay | Conversation only |
| 40 to 59 | Column only, compact header, no cards | Conversation only |
| below 40, or height below 10 | A plain "Please enlarge this window" message | Same |

Height: below 20 lines hide the footer and disclaimer. Below 14 lines reduce the
suggestions to one-line rows or hide them. The composer and the conversation
must always remain usable. Every frame must fit the window exactly, with no line
wider than the terminal and no vertical overflow. Tests enforce this across
sizes.

## Connection and loading states

| Source | Basic shows | Advanced shows |
|---|---|---|
| First connection | "Connecting…" | Same, with the gateway address |
| Network failure, loopback gateway | "Digital Commons isn't running on this computer yet." with how to start it | Address, error, retry countdown |
| Network failure, remote gateway | "The service can't be reached. Please contact your IT team." | Same detail |
| HTTP 401 | "You don't have access to this service. Please contact your IT team." | Same detail |
| `state: starting` | "Getting ready. The first response can take a few minutes." | Warm-up state and model |
| `unavailable`, `provider_unreachable` | "The AI service isn't responding." | Provider and error detail |
| `unavailable`, `model_missing` or `model_load_failed` | "The assistant isn't set up yet. Please contact your IT team." | Missing models with `install_hint` |
| `unavailable`, `cloud_access_enabled` | "The assistant isn't set up yet. Please contact your IT team." | "Ollama cloud access is on. Run `python dc.py setup`." |
| `degraded` | Looks ready | Detail in the inspector |
| `demo` | `● Demo mode` in the header, and each reply says it is a demonstration | Same |

The composer stays enabled in every state, and a failed send shows inline. A reply
proves the assistant is ready, so the `starting` line is hidden while a reply is
writing text, and until a status asked for after that reply arrives. The terminal
asks for one as soon as each reply ends, and otherwise polls every 5 seconds. It
retries bootstrap until it succeeds, re-fetches it after any failed poll or when
`gateway.instance_id` changes, and recovers without a restart. Terminal config is
found through `--config`, then `DC_TERMINAL_CONFIG`,
then `os.UserConfigDir()/DigitalCommons/terminal.yaml`, then built-in defaults. Theme
and mouse choices persist to `state.json` beside that file. Flags: `--config`,
`--gateway`, `--version`.

## Vocabulary

| Avoid in Basic mode | Use |
|---|---|
| Model, LLM, inference | The assistant, Digital Commons |
| Gateway, backend, server, Ollama | The service, the AI service |
| Token, context window | (Omit) "This document is too long to read in full." |
| Prompt | Message |
| Conversation context reset | New chat |

A file's name is the person's own, so a notice about a file (`attachment_truncated`,
`attachment_left_out`) is shown as sent, never reworded or hidden, even when the
name holds one of these words.

## Security in the terminal

`docs/security.md` is the authority. In summary:

- All text from the gateway, the model or a document is untrusted and passes through
  one `Clean` function before display, Markdown rendering or copying.
- Links are shown in full and are never emitted as terminal hyperlinks.
- Nothing the model produces is ever executed.
- Attachments are read only when the user chooses them, and are limited to the
  gateway's accepted extensions and size.
- The gateway URL must be `http` only for loopback addresses, `https` otherwise,
  unless `gateway.allow_insecure_http` is set. The token comes from
  `DC_GATEWAY_TOKEN`, otherwise `<data_dir>/local-token` when the gateway is
  loopback, and never from a config file. The local token is never sent to a
  non-loopback gateway.
- Redirects are refused.
- The indicator never says "On this computer" unless the gateway URL is loopback.
