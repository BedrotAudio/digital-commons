# Mac checklist

`.github/workflows/tests.yml` runs the tests on GitHub's Mac machines, Apple Silicon and
Intel, including the real terminal in a pseudo terminal, and the cloud gate against a
real Ollama and a real small model. All of it passes there. Those machines have no
screen, keyboard, mouse or permission dialogs, so these checks need a person at a real
Mac. None of them has been done yet.

Set up first with `python3 dc.py setup`, then use `python3 dc.py demo` for everything
below unless a step says otherwise. Note the macOS version, the chip, and each
terminal's version with your results.

## Colors

- [ ] **Terminal.app before macOS 26, 256 colors.** In both themes (`/theme light`,
  `/theme dark`) the whole window is painted, with no stripes of the terminal's own
  background, and all text is readable. Include the sidebar, a code block, and the
  Privacy and Settings panels. Widen and narrow the window and look again.
- [ ] **iTerm2 and Ghostty, true color.** The same check. The canvas should match the
  screens from the test run.

## Keys

- [ ] **Terminal.app:** the footer says `Ctrl+J new line`, and Ctrl+J starts a new line.
  Shift+Enter sends, because Terminal.app does not report it.
- [ ] **iTerm2:** Shift+Enter starts a new line and does not send.
- [ ] **Laptop keyboard:** the footer names `/help`, not F1. F1 to F3 work with Fn.
- [ ] **Terminal.app:** with focus on the conversation, Up, Down, Home and End scroll it.
  Page Up and Page Down belong to Terminal.app.
- [ ] **Cmd+V:** pasting several lines puts them in the composer and sends nothing.
- [ ] **Ctrl+C:** stops a reply. Twice when idle quits, and the shell comes back with a
  visible cursor and no leftover mouse reporting.

## Mouse

- [ ] **Wheel:** scrolls whatever is under the pointer.
- [ ] **Terminal.app:** Fn+drag selects text. Help names Fn+drag.
- [ ] **iTerm2:** Option+drag selects text. Help names Option+drag.
- [ ] **VS Code:** Help says to turn on Mac Option Click Forces Selection, which is off
  by default. With it on, Option+drag selects text.
- [ ] **Copy button:** click Copy on a reply, then paste into another app. The text
  matches, and a code block's Copy gives only that block.

## Files

- [ ] **Permission prompt:** `/attach`, then open Desktop, Documents and Downloads. macOS
  asks once for each. Allow one and the folder lists.
- [ ] **Refusal:** in System Settings, Privacy & Security, Files and Folders, turn one of
  those off. The picker shows a plain message naming that setting.
- [ ] **Other volumes:** the `/Volumes` row opens a connected drive.
- [ ] **Dragging a file:** type `/attach `, drag a `.txt` file whose name has a space from
  Finder into Terminal.app, and press Enter. The file is attached, although Terminal.app
  puts a backslash before each space. A path typed as `~/Desktop/...` works too.

## Ollama

For these, use `python3 dc.py dev` with the Ollama app from ollama.com.

- [ ] **Menu bar restart:** with cloud access on, `python3 dc.py setup` explains the change
  and asks. After quitting from the menu bar and opening Ollama again,
  `python3 dc.py doctor` says cloud access is off, with source `config`.
- [ ] **launchctl:** with the setting removed from `~/.ollama/server.json`, run
  `launchctl setenv OLLAMA_NO_CLOUD 1` and restart Ollama. `doctor` says off, with source
  `env`. After a Mac restart it is on again, which is what the printed note says.
- [ ] **Speed:** on Apple Silicon, record the time to first word and words per second,
  so the README can replace its estimate with a measured figure.

## Starting and stopping

- [ ] **Closing the window:** close the Terminal window during `dev`, confirm, then run
  `python3 dc.py doctor` in a new window. The gateway is not running.
- [ ] **Force quit:** quit the terminal app outright during `dev`. Within about 10 seconds
  the same check shows no gateway running.
- [ ] **A slow start:** the Privacy panel opens without a long pause. It looks up this
  computer's own name, which a Mac can take seconds to answer on some networks.
- [ ] **Old Python:** on a Mac with only Apple's `python3`, `python3 dc.py setup` prints the
  `brew install python@3.12` instruction. After that, the same command works.

## A terminal copied from elsewhere

- [ ] Copy `dist/digital-commons-macos-arm64` from a Windows `python dc.py build --all`
  to a Mac. By AirDrop or download it is blocked until signed and notarized. Say what
  the message is, so the README can quote it.
