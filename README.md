# Digital Commons

Digital Commons is a private AI workspace that runs in a terminal window. A public
employee types a question, attaches a text document or asks about approved
documents, and gets an answer from an AI model that runs on the same computer. The
conversation does not go to an outside AI service. A later step lets the model run on
an organization server instead.

It has two ways of working, over the same program:

- **Basic mode** is the default. It looks and behaves like the AI chat tools most
  people already know: a list of past chats on the left, suggested tasks on the
  home screen, a message box at the bottom, and a reply that appears as it is
  written. It does not use words such as model, token or gateway.
- **Advanced mode** is the same program with the covers off. It adds choice of
  model tier, a panel with timing and token counts, code blocks you can copy, and
  diagnostics.

**This is beta software. It is not approved for production use, it is not approved
for sensitive data, and it makes no claim of compliance with any government
framework.** Running a model locally does not by itself make an application secure
or compliant. See [Privacy](#privacy) and [docs/security.md](docs/security.md).

## How it fits together

```
Terminal (Go)  ->  Gateway (Python, FastAPI)  ->  Ollama (local model)
```

The terminal talks only to the gateway. The gateway is the only part that talks to a
model server. The gateway and Ollama listen only on the loopback address `127.0.0.1`,
which other computers cannot reach. [docs/architecture.md](docs/architecture.md) has the details.

## Platform status

Windows and macOS are the two supported workstation platforms. Ubuntu is the future
organization server, not a workstation target.

| Platform | What has been run |
|---|---|
| Windows 11 | The gateway and terminal tests, the `dc.py` tooling tests, and the terminal end to end test through a Windows pseudo console all pass. The end to end test runs `dc.py dev`, `demo`, `backend` and `frontend` for real, and covers the organization relay, a gateway restart, Ollama with cloud access on, and Ollama not answering, using a stand-in for Ollama. Replies from the configured model on a CPU-only computer were measured, and the figures are in [docs/architecture.md](docs/architecture.md). |
| macOS (Apple Silicon and Intel) | GitHub's Mac machines pass every automated test: the gateway, tooling and terminal tests, and the real terminal in a pseudo terminal against the real gateway. A separate job installs Ollama with Homebrew and leaves its cloud access on: the gateway reports it, refuses chat, stores nothing and loads no model. It then turns cloud access off with `python3 dc.py setup --disable-ollama-cloud` and gets a reply from a small real model through the gateway. Those machines install Python and Go with GitHub's own setup steps rather than Homebrew, and have no screen, keyboard or permission dialogs, so no person has used it at a real Mac yet. [docs/mac-checklist.md](docs/mac-checklist.md) lists what still needs one. |
| Linux | Not a workstation target. GitHub's Ubuntu machine passes every automated test, including the real terminal in a pseudo terminal. |

## Requirements

- Windows 11, or macOS. Windows 11 is the version that has been tested.
- Python 3.11 or newer. The tests run on 3.12.
- Go 1.26 or newer, to build the terminal. The tests run on 1.27.3. Go is used only
  to build. The finished terminal is a single program.
- [Ollama](https://ollama.com/download), to run a model on this computer. Not needed
  for [demo mode](#demo-mode).
- Disk space for a model. The configured one, `Gemma4:12b`, is 8.0 GB as installed.
- An internet connection for setup, to download Python packages, Go modules and the
  model. After that, chat does not need one.

No NVIDIA graphics card is needed. Without a graphics card, replies are slow. On the
development computer (8 cores, 32 GB of memory, no graphics card) `Gemma4:12b`
produced about 4 to 6 tokens (word pieces) per second, and loading the model the
first time took about 200 seconds. Other hardware is untested.

You do not need make, Node.js or Docker.

## Install and start on Windows

Use Windows Terminal or PowerShell.

1. Install Python and Go. Open a new window afterwards so they are found.

   ```
   winget install Python.Python.3.12
   winget install GoLang.Go
   ```

   If typing `python` opens the Microsoft Store, Python is not installed yet. Install
   it as above and open a new window.

2. Install Ollama from <https://ollama.com/download> and start it.

3. Download the model. This is about 8 GB.

   ```
   ollama pull Gemma4:12b
   ```

4. Get the code and set it up.

   ```
   git clone https://github.com/BedrotAudio/digital-commons.git
   cd digital-commons
   python dc.py setup
   ```

   If you do not use git, use **Code > Download ZIP** on the GitHub page, unzip it and
   open a terminal in that folder.

   `setup` creates a Python environment in `backend/.venv`, installs the packages,
   builds the terminal to `bin/digital-commons.exe`, and checks Ollama. It may ask
   you one question about turning off Ollama cloud access. See
   [Ollama cloud access](#ollama-cloud-access).

5. Check that everything is ready, then start.

   ```
   python dc.py doctor
   python dc.py dev
   ```

   `dev` starts the gateway in the background, waits until it answers, opens the
   terminal, and stops the gateway when you quit the terminal. Quit with `/exit` or
   by pressing Ctrl+C twice.

The folder also holds `dc.cmd`, so `.\dc dev` does the same as `python dc.py dev`, in
PowerShell and in Command Prompt. In Command Prompt, plain `dc dev` also works unless
the computer is set to skip the current folder when looking for programs.

## Install and start on macOS

GitHub's Mac machines run `python3 dc.py setup`, `doctor` and `test` successfully, but no person has followed these steps at a real Mac yet, and the Homebrew step is unverified. See [Platform status](#platform-status).

1. Install Python 3.11 or newer and Go. With [Homebrew](https://brew.sh):

   ```
   brew install python@3.12 go
   ```

   The `python3` that comes with the Apple developer tools may stay older than 3.11
   after this, because Homebrew installs this version as `python3.12`. That is fine:
   `dc.py` starts on the old Python and hands over to the newer one. Without a newer
   one, it prints this instruction instead.

2. Install Ollama from <https://ollama.com/download> and start it.

3. Download the model. This is about 8 GB.

   ```
   ollama pull Gemma4:12b
   ```

4. Get the code and set it up.

   ```
   git clone https://github.com/BedrotAudio/digital-commons.git
   cd digital-commons
   python3 dc.py setup
   ```

5. Check that everything is ready, then start.

   ```
   python3 dc.py doctor
   python3 dc.py dev
   ```

On macOS and Linux, `sh scripts/setup.sh`, `sh scripts/start.sh` and
`sh scripts/stop.sh` do the same as `setup`, `dev` and `stop`.

Apple Silicon Macs run Ollama models on the graphics hardware, so replies should be
much faster than the Windows figures above. That has not been measured here.

## Ollama cloud access

Ollama can pass a chat to a remote service. Digital Commons refuses to use Ollama
while that is possible, because it would send what you type off the computer. Until
the setting is off, chat is unavailable and the terminal says the assistant is not
set up.

`python dc.py setup` checks this. If cloud access is on, it explains what the
setting does, shows exactly what it will change in `~/.ollama/server.json`, and asks
yes or no. On yes it adds `"disable_ollama_cloud": true` to that file, keeps every
other setting already in it, and saves a copy of the old file next to it with `.bak`
added. You then have to restart Ollama yourself. Quit it from the system tray (Windows)
or menu bar (macOS) and start it again. On a Mac, if you started it with `ollama serve`
or `brew services`, restart that instead.

- `python dc.py setup --disable-ollama-cloud` answers yes in advance.
- If setup is not running in an interactive window and you did not pass that flag, it
  changes nothing and prints how to do it.
- Setting the environment variable `OLLAMA_NO_CLOUD=1` for Ollama has the same effect.
  The macOS menu bar app does not see variables set in a terminal, so there it is
  `launchctl setenv OLLAMA_NO_CLOUD 1` followed by a restart of Ollama, and macOS forgets
  it when the Mac restarts. The file above lasts, so prefer that.
- `python dc.py doctor` shows the current state as "Cloud access".

Turning cloud access off stops Ollama's cloud models for every program that uses this
copy of Ollama. Models installed on this computer keep working.

## Choosing and configuring a model

Model names are in `config/models.yaml`, not in the code. The file has a catalog for
each provider. The one used with Ollama is `catalog.ollama`, and it names a model
for each of three tiers:

| Tier | Meant for | In the shipped file |
|---|---|---|
| `fast` | Quick questions and simple tasks | `Gemma4:12b`, short replies |
| `standard` | General everyday work | `Gemma4:12b` |
| `advanced` | Complex reasoning and coding | `Gemma4:12b`, with reasoning on |

All three use the same model, because a computer with 32 GB of memory should not be
asked to hold three at once. The tiers differ in how much they may write
(`max_output`), how varied the wording is (`temperature`) and whether the model
reasons before answering (`think`). Reasoning improves hard answers and makes a CPU
wait a long time, so automatic routing never chooses a tier with `think: true`. In
Advanced mode you choose it yourself with `/model advanced`.

Basic mode always uses automatic routing. Advanced mode can use automatic routing or
a tier of your choice.

### Use a different model

1. Find a model on <https://ollama.com/library> and download it, for example
   `ollama pull <name>`.
2. Edit `config/models.yaml`. Change `name:` under `fast`, `standard` and `advanced`
   in `catalog.ollama` to the new name. Use a different model per tier only if the
   computer can load them one at a time.
3. Keep `context` the same for tiers that share a model. Ollama reloads a model when
   the context size changes, and each reload is slow. Do not set `context` below 2048.
   Keep `max_output` well below `context`.
4. Check whether the new model can reason: its Ollama library page lists `thinking`
   among its capabilities. If it cannot, set `think: false` under `advanced`, because
   Ollama refuses a request for reasoning from a model without it. `/model advanced`
   would otherwise fail every time, with an error detail that names this setting.
5. Restart the gateway. Quit the terminal and run `dev` again, which starts a fresh
   gateway each time. A gateway started on its own with `backend` is stopped with
   `python dc.py stop`.
6. Run `python dc.py doctor`. The "Model" line says whether the configured name is
   installed.

### Use a smaller model on a smaller computer

The size of the download is a rough guide to the memory a model needs while it runs,
plus room for the conversation. A computer with less memory than the 8 GB model
needs, or one that is too slow with it, should use a model with fewer parameters.
Each model page on the Ollama library lists its available sizes and the download size
of each. Pick one that is well under the computer's free memory, download it, and
change the three `name:` lines. If the model cannot reason, also set `think: false`
under `advanced`, as step 4 above explains. A smaller model is faster and gives weaker
answers.
Which sizes exist and how they behave depends on the model, so check the library page
rather than guessing. Only `Gemma4:12b` has been tested with this project.

If you point the project at a name that is not installed, `doctor` says so and gives
the `ollama pull` command to run.

### Local settings

Do not edit `config/development.yaml` for settings that belong to one computer.
Create `config/development.local.yaml`, which has the same layout, holds only what you
change, and is ignored by git. For example, to use port 8010:

```yaml
gateway:
  port: 8010
```

`dev` points the terminal at the new port by itself. If you run the gateway and the
terminal separately with `backend` and `frontend`, also change the port in
`gateway.base_url` in `config/terminal.yaml`, because `frontend` takes the address
from that file.

## Try Basic mode

Run `python dc.py dev` (or `python3` on macOS). The terminal opens in Basic mode. The
header shows `On this computer` when the gateway reports that the conversation stays
on this computer. The first reply can take several minutes while the model loads, and
the screen says so.

Things to try:

1. **Use a suggestion.** Click **Draft correspondence**. Without a mouse, press Tab until
   the cards have focus, move with the arrow keys and press Enter. It fills the message
   box with a start. Finish the sentence and press Enter.
2. **Summarize a document.** Type `/attach data/sample_documents/remote-work-policy.md`
   and press Enter, then type `Summarize this document` and press Enter. The path is
   relative to the project folder, which is where `dc.py` starts the terminal. You can
   also press Ctrl+O to pick a file. The sample documents are invented: the City of
   Example does not exist.
3. **Find information in approved documents.** Type
   `/search What are the sealed bid thresholds?` The answer should draw on the
   Procurement Thresholds sample, cite it as [1], and list it in a Sources block,
   labelled as an invented sample. A document is labelled this way when its front
   matter has `sample: true`, and the model is told the same.
4. **See permissions at work.** Type `/search How does an investigator interview a
   complainant?` The sample Personnel Investigations document exists, but it is
   limited to the `hr` group and the development user is not in it, so the gateway
   removes it before anything is searched and the assistant cannot quote it.
5. **Manage chats.** Press Ctrl+N for a new chat. Earlier chats are in the left list,
   and Ctrl+B hides it. `/delete` removes the current chat after you confirm.
6. **Look at privacy.** `/privacy` opens a panel that says where processing happens
   and what is stored. `/theme light` and `/theme dark` change the colours.
7. **Resize the window.** The layout adapts down to a width of about 40 columns.

Type `/help` or press F1 at any time. Press `/` in an empty message box to see every
command.

## Try Advanced mode

Type `/mode developer` (`/mode advanced` also works), or press F2 and choose Advanced.
To start in Advanced mode, set `application.default_mode: developer` in
`config/terminal.yaml`. Type `/mode basic` to go back. Switching mode changes what is
shown and never what you are allowed to do: permissions are decided by the gateway.

Things to try:

1. **Watch a reply being produced.** Send any message. The line above the reply shows
   the tier and, when the reply ends, tokens per second and the number of tokens.
2. **Open the inspector.** Press F3 (on a window narrower than 120 columns it opens as
   an overlay). It shows the route chosen, the model, the context meter, the token
   counts and timing, and the state of the gateway and Ollama.
3. **Choose a tier.** `/model fast`, `/model standard`, `/model advanced`, or
   `/model auto` to let the router decide. `/model` with no argument opens a picker.
   `advanced` turns reasoning on: the reasoning appears as dimmed text, and on a
   CPU-only computer it can take many minutes.
4. **Use the coding assistant.** Type `/code`, then ask for a short function. Code
   blocks are highlighted and numbered. `/copy code 1` copies the first one and Ctrl+Y
   copies the latest. Type `/code` again to turn it off.
5. **Read the diagnostics.** `/status` shows the full status.

Advanced mode never runs code or shell commands, and nothing the model writes is
executed.

## Demo mode

```
python dc.py demo
```

Demo mode needs no model and no Ollama. The gateway uses its `mock` provider, which
writes a fixed demonstration reply. **The replies are simulated: no AI writes them.**
The header says `Demo mode`, and each reply begins by saying it is a demonstration.

Demo conversations are kept in a `demo` folder inside the data folder, apart from real
conversations, and use their own gateway. If a normal gateway is already running, demo
mode asks you to stop it first with `python dc.py stop`.

In a demo, the reply repeats your question, lists any files you attached, shows the
passages found for a `/search`, shows a sample code block when `/code` is on, and shows
simulated reasoning with `/model advanced`. Everything in Basic and Advanced mode above
can be tried this way, except that the content of the replies is not real.

## Running the tests

After `setup`:

```
python dc.py test
```

This runs three sets and prints one line for each at the end:

- `pytest tests/backend`: the gateway, against a fake Ollama. Nothing reaches a real
  model.
- `pytest tests/tooling`: `dc.py` itself, with its data folder and `~/.ollama` pointed
  at temporary folders.
- `go test ./...` in `terminal/`.

The exit code is 0 only if all three pass. The suite takes several minutes. See
[docs/development.md](docs/development.md) to run one set or one test.

## Commands

Run `python dc.py <command>` (`python3` on macOS), or `.\dc <command>` on Windows, or
`make <command>` if you have make.

| Command | What it does |
|---|---|
| `setup` | Create `backend/.venv`, install the packages, build the terminal, check Ollama and offer to turn off cloud access. Takes `--disable-ollama-cloud`. |
| `dev` | Start the gateway in the background, wait for it (up to 30 seconds), run the terminal in the foreground, and stop the gateway when the terminal exits. If a gateway is already running it is used and left running. |
| `demo` | The same as `dev` with simulated replies and a separate `demo` data folder. No model needed. |
| `backend` | Run the gateway in the foreground. Press Ctrl+C to stop it. |
| `frontend` | Build the terminal if its source changed and run it with `config/terminal.yaml`, which also gives the gateway's address. Does not start a gateway. |
| `test` | Run the backend, tooling and terminal tests and report each. |
| `build` | Build the terminal to `bin/digital-commons.exe` (Windows) or `bin/digital-commons`. Takes `--all` to cross-compile it to `dist/` for macOS arm64, macOS amd64 and Windows amd64, with CGO off. That is the terminal only: the gateway has to be set up on the Mac itself with `python3 dc.py setup`. |
| `stop` | Stop a gateway that `dc.py` started. |
| `clean` | Remove `bin/`, `dist/`, caches and the pid file. Takes `--all` to remove `backend/.venv` too. Never removes `.tools` or your data folder. |
| `doctor` | Print a report: Python, Go, the environment, the terminal build, Ollama, the model, cloud access, the gateway and its port, and the data folder. |

`dc.py` is one file and uses only the Python standard library, so it runs before
anything is installed. It finds Go on `PATH` first and then in `.tools/go/bin`. It
never downloads Go. If Go is missing it prints the install command for your platform.

## Switching to the organization model server

No organization model server exists yet. These are the two supported ways to use one
when it does. Neither needs a code change. [docs/architecture.md](docs/architecture.md)
explains the difference.

### Relay: each computer keeps its own gateway

The terminal keeps talking to the local gateway, and the gateway sends the model
requests to the organization server. Conversations stay on each workstation. This is
the route for several staff in Beta 1.

Create `config/development.local.yaml`:

```yaml
inference:
  default_provider: org_server
providers:
  org_server:
    base_url: https://ai.example.gov   # the organization server
    ca_bundle:                         # optional path to the organization's CA certificate
security:
  allowed_upstream_hosts:
    - ai.example.gov                   # the same host name
```

Then set the token in the environment of whoever starts the gateway, and start as
usual:

```
set DC_ORG_SERVER_TOKEN=<token from your administrator>      (Windows Command Prompt)
$env:DC_ORG_SERVER_TOKEN = "<token>"                          (Windows PowerShell)
export DC_ORG_SERVER_TOKEN='<token>'                          (macOS)
```

The server must be reached over HTTPS, its host must be listed in
`allowed_upstream_hosts`, and the token variable must be set, or the gateway refuses
to start and says which is missing. The header then reads `Organization server`. The
tiers for this provider are in `catalog.org_server` in `config/models.yaml`; the
server picks its own models and these entries only set limits. The server must
implement the two routes described under "Model host contract" in
[docs/api.md](docs/api.md): `GET /healthz`, which the gateway asks before every reply,
and the streaming route. Nothing is stored on it.

Existing local conversations refuse new messages after the switch, because they began
under a different location. Start a new chat.

### Direct: the terminal talks to a gateway the organization hosts

Edit `config/terminal.yaml`:

```yaml
gateway:
  base_url: https://gateway.example.gov
```

Set `DC_GATEWAY_TOKEN` to the token you were given, then run the terminal on its own
with `python dc.py frontend`. Do not use `dev` for this: it starts a local gateway and
points the terminal at it. A gateway that is not on this computer must use `https`,
unless `allow_insecure_http: true` is set, which is for laboratories only. The
terminal then shows organization text and ignores any claim that processing is local.

This route gives everyone who uses the same token one identity and one chat history,
so it is for a single tester until per-user authentication exists.

## Privacy

What the beta does:

- Inference is local. There is no external AI service and no fallback to one.
- The gateway listens only on `127.0.0.1` and refuses any other address.
- Every request needs a token. The gateway makes one on first start and keeps it in the
  data folder. Web pages in a browser cannot call the gateway.
- Message text, titles and file names are not written to logs.
- Conversations are stored in a SQLite file in the data folder, outside the project
  folder, and removed after 30 days. Deleting a chat removes it from the database.
- The gateway refuses to start if the data folder would be synced by OneDrive, or on
  macOS by iCloud Drive or similar.
- No telemetry or analytics are sent anywhere.

What it does not do, stated plainly:

- **It is not approved for production use or for sensitive data, and it claims no
  compliance with any government framework.**
- Conversations are not encrypted on disk. Other software running as the same user can
  read them.
- Deleted data can survive on SSDs and in backups, such as Time Machine.
- Ollama itself may contact ollama.com to look for updates. No conversation text is
  involved.
- Anyone who can edit the configuration files can change what the gateway allows.

The full threat model, what is enforced and what is accepted for the beta is in
[docs/security.md](docs/security.md).

## Known limitations

- Beta software, as above. Not production-approved. No claim of compliance.
- **Attachments are text only.** Files must end in `.txt`, `.md`, `.csv` or `.tsv`, with
  at most 4 files of 256 KB each. PDF and Word files are refused: save them as text
  first. A spreadsheet must be saved as CSV.
- **Text encoding.** The terminal reads UTF-8 (with or without a byte order mark) and
  UTF-16 with a byte order mark. Any other file is read as Windows-1252, the usual
  encoding of older Windows files and of CSV files saved by Excel. A file in a different
  encoding, such as an Eastern European or Asian code page, is not detected and will
  show wrong characters without an error.
- **Documents.** The searchable collection is four invented sample documents, found by
  keyword matching. There is no ingestion of your own documents, no OCR and no
  semantic search yet.
- **One user.** The beta has a single development identity, `local.developer`, whose
  groups and permissions come from `config/development.yaml`. There are no per-user
  accounts.
- **Slow without a graphics card.** One request runs at a time. A cold model load and a
  long document can each take minutes.
- **Routing is simple.** Automatic routing is a set of plain rules, not an intelligent
  system.
- **No organization server yet.** Relay was tested against a second copy of the gateway
  acting as the server, not a real one. Direct is for a single tester.
- **Keyboard and mouse.** Shift+Enter for a new line works in Windows Terminal. Elsewhere
  use Ctrl+J. The mouse is captured by the terminal, so selecting text needs a modifier
  key (Shift+drag in most terminals on Windows, Option+drag in iTerm2, Fn+drag in
  Terminal.app). VS Code on a Mac selects with Option+drag only after you turn on its
  Mac Option Click Forces Selection setting. Set `appearance.mouse: false` in
  `config/terminal.yaml` to turn the mouse off. The window must be at least 40 columns
  by 10 rows.
- **Not checked by hand.** A real Shift+Enter in Windows Terminal, pasting from the real
  clipboard, and copying through the terminal's clipboard escape sequence were not
  confirmed by hand. Check them on your terminal.
- **Binaries are unsigned.** See [Endpoint protection](#endpoint-protection).
- **Tested with Ollama 0.40.13.** An Ollama that does not report its cloud setting is
  refused, so chat is unavailable until it is updated.
- **No code execution.** Isolated code execution is a future feature.

## Endpoint protection

The terminal and the Go toolchain are not code-signed, and the terminal is built on
your own computer, so no security product has seen it before. Some endpoint protection
and antivirus products quarantine or block such programs, and this happened during
development. A managed computer may have a stricter product.

Signs that it happened:

- `python dc.py build` or `setup` says the build failed, or that it finished but
  `bin/digital-commons.exe` is missing.
- Go reports "Access is denied" or "The system cannot execute the specified program".
- The terminal started yesterday and now will not start, or the file has disappeared
  from `bin/`.

What to do:

1. Open the product's quarantine or protection history. Restore the file and choose the
   option that excludes it from future scans.
2. Add exclusions for the folders involved: `bin/` in this project, `.tools/` if you
   keep Go there, and Go's build cache, whose location is printed by `go env GOCACHE`.
   On a computer managed by an organization, ask the IT team to do this. Do not turn
   protection off altogether.
3. Run `python dc.py build` again.

`python dc.py doctor` reports whether `bin/digital-commons.exe` exists. It cannot tell
you why it is missing.

On macOS, a program you build yourself runs without notarization. A binary built by
someone else and copied to a Mac would be blocked by Gatekeeper until it is signed with a
Developer ID and notarized, which this project does not do yet. That includes what
`dc.py build --all` puts in `dist/`, so `python3 dc.py setup` on the Mac is the way to
install there.

## Project layout

```
dc.py            Task runner. Standard library only.
dc.cmd           Lets Windows users type "dc dev".
Makefile         Thin wrapper over dc.py, for people who prefer make.
scripts/         Shell wrappers for macOS and Linux: setup.sh, start.sh, stop.sh.
terminal/        The Go terminal (Basic and Advanced mode). No model-specific logic.
backend/         The FastAPI gateway: providers, router, retrieval, conversations.
config/          development.yaml, models.yaml and terminal.yaml.
data/            Sample documents only. Runtime data lives outside the project.
tests/           backend/ and tooling/. Go tests sit beside their code in terminal/.
docs/            Design and operations documents.
bin/             The built terminal. Created by setup. Not committed.
```

Runtime data (conversations, the token, the pid file and the gateway log) lives in
the data folder, which is not inside the project:

| Platform | Data folder |
|---|---|
| Windows | `%LOCALAPPDATA%\DigitalCommons` |
| macOS | `~/Library/Application Support/DigitalCommons` |
| Linux | `$XDG_STATE_HOME/digital-commons`, or `~/.local/state/digital-commons` |

Set `DC_DATA_DIR` to an absolute path to use another folder. On Windows, keep it inside
your user folder or `%LOCALAPPDATA%`: a folder elsewhere usually lets other accounts on
the computer read it, and the privacy panel says so.

## More documentation

- [docs/development.md](docs/development.md): working on the code, every setting and
  environment variable, debugging, adding a provider or a model.
- [docs/deployment.md](docs/deployment.md): a future-facing guide to the Ubuntu pilot.
  Mostly not built yet.
- [docs/roadmap.md](docs/roadmap.md): what Beta 1 delivered and what comes next.
- [docs/requirements.md](docs/requirements.md): what the product must do.
- [docs/architecture.md](docs/architecture.md): components, request flow, deliberate
  changes from the brief.
- [docs/api.md](docs/api.md): the terminal-to-gateway contract and the model host
  contract.
- [docs/ui-spec.md](docs/ui-spec.md): the terminal interface.
- [docs/security.md](docs/security.md): the threat model and what is enforced.
- [docs/implementation-notes.md](docs/implementation-notes.md): verified library and
  Ollama facts.
