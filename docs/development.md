# Development Guide

For people working on the code. The README covers installing and trying the product.
Commands are written for Windows with `python`. On macOS use `python3`, and replace
`backend\.venv\Scripts\python.exe` with `backend/.venv/bin/python`.

## What `setup` does

`python dc.py setup` runs three steps and can be repeated safely:

1. Creates `backend/.venv` with the Python that is running `dc.py`, then runs
   `pip install -r backend/requirements-dev.txt`. The requirements are pinned to exact
   versions. An existing `backend/.venv` is reused if its Python can run pip. One left
   without pip, by a create that failed or was interrupted, is rebuilt.
2. Builds the terminal into `bin/`.
3. Checks Ollama at the address in `config/development.yaml`: whether it answers,
   whether the models in `config/models.yaml` are installed, and whether cloud access
   is off. If it is on, it offers the `~/.ollama/server.json` change described in the
   README.

If step 2 fails, the other steps still run and `setup` exits with 1 at the end.

The same work by hand:

```
python -m venv backend\.venv
backend\.venv\Scripts\python.exe -m pip install -r backend\requirements-dev.txt
cd terminal
go build -trimpath -o ..\bin\digital-commons.exe .\cmd\digital-commons
```

`dc.py` sets `GOTOOLCHAIN=local` and `CGO_ENABLED=0` for Go unless they are already
set. `GOTOOLCHAIN=local` stops Go fetching a newer toolchain by itself. The terminal
needs the Go version named in `terminal/go.mod`, and `doctor` checks it.

### Finding Go

`dc.py` looks for `go` on `PATH`, then in `.tools/go/bin` in the repository. It never
downloads Go. If neither is found it prints the install command: `winget install
GoLang.Go` on Windows, `brew install go` on macOS, `sudo snap install go --classic`
elsewhere. `dc.py` prints these and does not run them, and they have not been tested
here. `.tools/` is ignored by git and `clean` never touches it.

## Running the pieces separately

Use two windows to see the gateway's output as it happens.

Window 1:

```
python dc.py backend
```

This runs `python -m app` from `backend/` with the virtual environment's Python, in
the foreground. Ctrl+C stops it. To run it with no wrapper at all:

```
cd backend
.venv\Scripts\python.exe -m app
```

`python -m app` accepts `--host` and `--port`. The host must still be a loopback
address. Beta 1 refuses anything else, in every identity mode.

Window 2:

```
python dc.py frontend
```

This builds the terminal if any `.go` file, `go.mod` or `go.sum` is newer than the
binary, then runs `bin\digital-commons.exe --config config\terminal.yaml`. Unlike `dev`,
it passes no `--gateway`, so the terminal uses `gateway.base_url` from
`config/terminal.yaml`. If you changed `gateway.port`, change the port in that address
too, or the terminal looks for the gateway on 8000. The terminal needs the gateway's
token. In the default `local` identity mode it reads `<data folder>\local-token`, which
the gateway creates on first start, and only when its gateway address is loopback.

The terminal must run in an interactive console. With piped input or output it says so
and exits with code 1. It takes two flags:

| Flag | Meaning |
|---|---|
| `--config <path>` | A `terminal.yaml`. Without it the terminal uses `DC_TERMINAL_CONFIG`, then `terminal.yaml` in the user config folder under `DigitalCommons`, then built-in defaults. |
| `--gateway <url>` | Replaces `gateway.base_url` for this run. `dc.py dev` passes it. |

### How `dev` and `demo` run things

- The terminal is built first, so a build failure does not leave a gateway running.
- The gateway starts with output going to `gateway.log` in the data folder (the `demo`
  folder inside it, for `demo`). A pid file, `gateway.pid`, records the process id, its
  start time, the mode and the log path. The start time stops a recycled process id from
  being mistaken for the gateway.
- `dc.py` polls `/healthz` for up to 30 seconds and reports every 5 seconds. If the
  gateway exits during that time, it prints the end of its log.
- If a gateway already answers, `dev` uses it and leaves it running. `demo` will not
  reuse a gateway that is not a demo gateway, and `dev` will not reuse a demo gateway.
- The terminal is started with the same standard input, output and error as `dc.py`,
  never a pipe. On Windows, resize events reach the program only through the real
  console input handle.
- When the terminal exits, or on Ctrl+C, Ctrl+Break, SIGTERM or SIGHUP, `dc.py` asks
  the gateway to exit, waits up to 10 seconds, then forces it. On Windows it sends a
  Ctrl+Break through a helper process and falls back to `taskkill /T /F`. On POSIX it
  sends SIGTERM and then SIGKILL. Because the virtual environment's `python.exe` on
  Windows is a launcher that starts the real interpreter, `dc.py` also puts itself in a
  job object that ends its children if `dc.py` is killed outright. macOS has no
  parent-death signal, so on macOS and Linux `dc.py` gives the gateway the read end of a
  pipe and keeps the write end. The kernel closes that end however `dc.py` ends, even on
  SIGKILL, and `backend/app/lifeline.py` then shuts the gateway down cleanly.

`stop` only stops a gateway that `dc.py` started. If something else answers on the
port it says so and leaves it alone.

## Configuration

| File | What it holds |
|---|---|
| `config/development.yaml` | Gateway settings: address, storage, providers, security allow-lists, the development identity and permissions, limits, retention, the suggestion cards. |
| `config/development.local.yaml` | Optional. Same layout. Merged over `development.yaml`: mappings merge key by key, and a list replaces the whole list. Ignored by git. |
| `config/models.yaml` | The tier catalog. Model names, context sizes, output limits, temperature and reasoning per provider and tier. |
| `config/terminal.yaml` | Client settings: default mode, the gateway address, theme and mouse. Never holds a model-server URL or a token. |

The files have comments that explain each setting. A mistake in the gateway files
stops it with a plain message beginning "Digital Commons cannot start", and nothing is
guessed. `dc.py` reads a few of the same values (the port, the provider, the Ollama
address and the model names) with a small reader of its own that handles only plain
`key: value` lines. It is not a YAML parser. Use that layout in these files.

The terminal also keeps the theme and mouse choices you make in the program, in a
`state.json` next to the `terminal.yaml` it loaded. That file is a per-person setting
and git ignores it. A choice made in the program wins over `terminal.yaml`, and only
the one you changed is kept, so changing the theme leaves `appearance.mouse` to
`terminal.yaml`.

### Environment variables

Gateway and `dc.py`:

| Variable | Effect |
|---|---|
| `DC_CONFIG` | Path to a gateway config file to use instead of `config/development.yaml`. A relative path is taken from the repository root. A `.local` file next to it is also read. |
| `DC_MODELS_CONFIG` | The same, for `config/models.yaml`. |
| `DC_PROVIDER` | The provider to use: `ollama`, `mock` or `org_server`. Overrides `inference.default_provider`. `dc.py demo` sets it to `mock`. |
| `DC_DATA_DIR` | The data folder. Must be absolute. Overrides `storage.data_dir`. |
| `DC_GATEWAY_TOKEN` | In `shared_token` identity mode, the secret the gateway checks, at least 32 characters. For the terminal, the token to send. `dc.py` removes it from the terminal's environment when the gateway is local, so a token meant for another gateway is not sent to this one. |
| `DC_ORG_SERVER_TOKEN` | The token for the `org_server` provider. The name comes from `providers.org_server.token_env`. |
| `DC_SERVICE_TOKEN` | The token this gateway requires when it stands in for the organization server (`inference_service.enabled`). At least 32 characters. |
| `LOCALAPPDATA` | Windows: the data folder is `%LOCALAPPDATA%\DigitalCommons`. |
| `XDG_STATE_HOME` | Linux: the data folder is `$XDG_STATE_HOME/digital-commons` when this is an absolute path. |

Terminal:

| Variable | Effect |
|---|---|
| `DC_TERMINAL_CONFIG` | Path to a `terminal.yaml`. Used when `--config` is not given. |
| `DC_GATEWAY_TOKEN`, `DC_DATA_DIR` | As above. The terminal finds the local token in the data folder. |
| `NO_COLOR` | Any non-empty value turns colour off. |
| `WT_SESSION`, `TERM`, `TERM_PROGRAM`, `SSH_CONNECTION` | Read to decide whether to request Windows win32-input mode, and which text-selection hint to show. |
| `TEA_TRACE`, `TEA_DEBUG` | Cleared at start. Bubble Tea would otherwise write every keystroke and reply to a file. |

Ollama (set on Ollama, not on this project):

| Variable | Effect |
|---|---|
| `OLLAMA_NO_CLOUD=1` | Turns off Ollama cloud access, like `disable_ollama_cloud` in `server.json`. |
| `OLLAMA_HOST` | The address Ollama listens on. Keep it on loopback. |

Tests only: `DC_E2E=1` enables the end to end test, `DC_E2E_SCREENS=<folder>`
makes it save every captured screen as `screens.html` and `screens.txt`, and
`DC_FRAMES_OUT=<file>` makes two terminal tests write their frames to a file.

`dc.py` also sets `PYTHONUNBUFFERED=1` for the gateway and `PYTHONPATH=backend` for the
backend tests.

### The data folder

| Platform | Default |
|---|---|
| Windows | `%LOCALAPPDATA%\DigitalCommons` |
| macOS | `~/Library/Application Support/DigitalCommons` |
| Linux | `$XDG_STATE_HOME/digital-commons`, else `~/.local/state/digital-commons` |

It holds `conversations.db` (SQLite, plus its write-ahead log), `local-token`,
`gateway.pid`, `gateway.log` and the `demo` folder. The gateway refuses a data folder
that a sync service would copy off the computer. `dc.py` uses the same rules as the
gateway, in `resolve_data_dir`, and a test keeps them equal.

When developing, set `DC_DATA_DIR` to a scratch folder so that your own conversations
are not touched:

```
set DC_DATA_DIR=%LOCALAPPDATA%\dc-scratch
python dc.py dev
```

On Windows, keep the folder inside your user profile or `%LOCALAPPDATA%`. A folder
elsewhere, such as `C:\temp`, usually lets every account on the computer read it, and
the privacy panel then says so instead of calling it private.

## Testing

```
python dc.py test
```

runs the three sets below and exits 0 only if all pass.

| Set | Command | Notes |
|---|---|---|
| Gateway | `backend\.venv\Scripts\python.exe -m pytest tests/backend` | Run from the repository root with `PYTHONPATH=backend`. Uses a fake Ollama server in `tests/backend/harness.py`. Never contacts a real one. |
| Tooling | `backend\.venv\Scripts\python.exe -m pytest tests/tooling` | Tests `dc.py`: data folder rules, the `server.json` merge, Go lookup, command line, Ollama checks against a fake, starting and stopping a real mock gateway, and the `dev` and `demo` sessions with a stand-in for the terminal. Also the GitHub workflow file, and the workflow's live check against a real gateway in front of a fake Ollama. |
| Terminal | `cd terminal` then `go test ./...` | Unit and screen tests. Uses a fake gateway. |

Run one test with pytest's usual selection, for example
`... -m pytest tests/tooling/test_server_json.py -k backup`, and one Go package with
`go test ./internal/attach/`.

The end to end test starts the real terminal in a pseudo terminal against a real gateway
and reads an emulated screen. It uses a pseudo console on Windows and a Unix pty on macOS
and Linux. No real model is involved: the gateway uses the mock provider, a second
gateway stands in for the organization server, and a small fake answers for Ollama.
Beyond the screens, it compares every API response and stream event with the Go types
and the examples in [api.md](api.md), runs `dc.py dev`, `demo`, `backend`, `frontend` and
`stop`, and samples the terminal's connections to show that only the gateway appears.

```
cd terminal
set DC_E2E=1
set DC_E2E_SCREENS=C:\temp\screens
go test ./internal/e2e/ -count=1 -v
```

It needs `backend/.venv`, `go` and `python` (`python3` on macOS and Linux) on `PATH`, and
port 8000 free for the `frontend` scenario, which skips itself otherwise. The scenarios
that are about win32-input mode are in `e2e_windows_test.go`, and the rest are written to
run everywhere. On macOS and Linux use `export DC_E2E=1`.
`.github/workflows/tests.yml` is set up to run it on every platform. It has only been
run on Windows.

A person still has to check what no test machine can. See
[mac-checklist.md](mac-checklist.md).

On a computer without the platform you changed, three checks go a long way:

- `GOOS=darwin go vet ./...` and `GOOS=linux go vet ./...` compile every test there.
- `tests/tooling/test_mac.py` and `test_old_python.py` inject the platform into `dc.py`.
- `TestRawOutputThroughTheEmulator` puts the program's unfiltered output through the
  emulator, which is what the harness sees on macOS and Linux with no console host in
  between.

Rules the tests rely on:

- Nothing reaches a real model. Real-model checks are manual.
- Nothing touches the real `~/.ollama`. The tooling tests point `HOME`, `USERPROFILE` and
  `DC_DATA_DIR` at temporary folders before each test. When trying the `server.json`
  step by hand, give it a copy.
- Test environments set `NO_COLOR` and `TERM` themselves. Some shells set them and
  change the colour level the terminal detects.

## Debugging

Check where things stand:

```
python dc.py doctor
```

It prints Python, Go, the environment, the terminal build, the provider, Ollama and its
version, whether the configured model is installed, cloud access, the gateway, the port
and the data folder. Each line is `ok`, `info` or `FAIL`. Ollama problems are `FAIL`
only when the active provider is `ollama`.

The gateway:

- Under `dev`, read `gateway.log` in the data folder. Under `backend`, output is in the
  window. Log lines hold the method, route, status, duration and request id. They never
  hold message text, titles or file names, so a bug report built from them has no
  conversation in it. Set `logging.level` in the config for more.
- `GET /healthz` needs no token and answers `{"status":"ok"}`:

  ```
  curl.exe -s http://127.0.0.1:8000/healthz
  ```

- Other routes need the token. On Windows PowerShell:

  ```
  $token = Get-Content "$env:LOCALAPPDATA\DigitalCommons\local-token"
  curl.exe -s -H "Authorization: Bearer $token" http://127.0.0.1:8000/v1/status
  ```

  `/v1/status` says whether the provider is reachable and whether the cloud gate
  passed. [api.md](api.md) lists every route.
- To get the OpenAPI schema, set `gateway.docs_enabled: true` in
  `config/development.local.yaml`, restart the gateway and run
  `curl.exe -s http://127.0.0.1:8000/openapi.json`. It needs no token. The gateway then
  also serves `/docs` and `/redoc`, but a browser cannot open them: browsers send a
  `Sec-Fetch-Site` header, and the gateway refuses any request that carries one, to keep
  web pages out. All of this is off by default. [api.md](api.md) is the contract.
- A refusal at startup, such as a non-loopback host, an unlisted upstream host or a
  missing token variable, exits with code 2 and a one-line reason.

The terminal:

- It writes no log, deliberately, because a log would hold conversation text. The Advanced
  mode inspector (F3) and `/status` show what it knows about the gateway.
- `appearance.mouse: false` in `config/terminal.yaml` helps when a terminal handles mouse
  reporting badly.
- If `dc.py` reports that the build finished but the file is missing, see "Endpoint
  protection" in the README.

Common problems:

| Symptom | Likely cause |
|---|---|
| "Port 8000 is in use by another program" | Another program holds the port. On Windows it can also be reserved by the system. Change `gateway.port` in `config/development.local.yaml`. `dev` follows it. For `frontend`, also change the port in `gateway.base_url` in `config/terminal.yaml`. |
| The terminal says the assistant is not set up | `doctor` will show why: cloud access on, the model not installed, or Ollama not running. |
| Replies take minutes to start | A cold model load on a CPU. The first message is slowest. `inference.warm_on_start` loads the model when the gateway starts. |
| The gateway will not start and names the data folder | The folder is inside OneDrive or another synced place. Set `DC_DATA_DIR`. |

## Adding a provider

A provider is how the gateway reaches a model. Beta 1 has three: `ollama`, `mock` and
`inference_api` (the organization server). Exactly one is active per gateway, so that
the privacy indicator stays truthful. To add one, for example a llama.cpp server:

1. **Implement the protocol.** Create `backend/app/providers/<name>.py` with a class that
   satisfies `InferenceProvider` in `providers/base.py`:

   | Member | Purpose |
   |---|---|
   | `name`, `kind`, `display_name`, `base_url`, `supports_warmup` | Identity. `kind` is the value used in config. |
   | `async health()` | A `ProviderHealth`: reachable, version, installed models, loaded models. |
   | `async preflight()` | Runs before a reply starts. Raise `ApiError` if no turn can start. |
   | `async cloud_gate()` | `True` or `False` if the provider has a gate like Ollama's, otherwise `None`. |
   | `stream(request)` | An async generator of chunks, ending with exactly one `FinalChunk`. |
   | `async warm(spec)` | Load a tier's model ahead of use. May do nothing. |
   | `install_hint(model)` | Text for the person fixing a missing model, or `None`. |
   | `async aclose()` | Release connections. |

   The request holds a `TierSpec` (model, context, output limit, temperature, reasoning)
   and a list of messages. Yield `StatusChunk`, `ThinkingChunk` and `DeltaChunk` as the
   model produces them. Report failures by raising `ApiError` with a code from the
   catalog in `app/models/errors.py`, such as `provider_unavailable`,
   `model_not_installed`, `timeout` or `provider_error`, and `ContextOverflow` when the
   prompt is too long. Do not put message text in an exception. Messages are never logged.

2. **Make HTTP clients with `create_upstream_client`** in `providers/client.py`. It ignores
   proxy settings and refuses redirects, which is how prompts are kept from leaving by
   accident. Treat any 3xx as `provider_error`, and never disable certificate checks.

3. **Register the kind.** Add it to the `kind` list in `ProviderConfig` in
   `app/config/settings.py`, add any new settings there, and add the rules for it in
   `_validate_providers`. Construct it in `build_provider` in `providers/factory.py`.

4. **Decide its locality.** `compute_locality` in `app/security/locality.py` returns
   `organization` for any kind it does not know, which is the safe default. Add a case
   that returns `this_device` only if the provider can prove that processing stays on
   this computer, as Ollama does with a loopback address plus its cloud gate. Add wording
   for the kind in `services/privacy.py`.

5. **Add its models.** Add `catalog.<provider name>` to `config/models.yaml` with the
   enabled tiers, and a block under `providers:` in `config/development.yaml`. Select it
   with `inference.default_provider` or `DC_PROVIDER`.

6. **Write tests** in `tests/backend`, using `test_ollama_adapter.py` and
   `test_mock_provider.py` as patterns, with a fake server in place of the real one. Test
   the failure paths: unreachable, a missing model, a redirect, a stream that stalls, and
   a cancelled request.

The terminal needs no change. It never sees the provider, only the tier, the locality
and the status that the gateway reports.

## Adding a model

Models are configuration.

1. Download it into Ollama: `ollama pull <name>`.
2. Edit `catalog.ollama` in `config/models.yaml`. Set `name` for each tier that should
   use it.
3. Keep `context` equal for tiers that share a model, because Ollama reloads a model when
   the context changes. Keep `max_output` smaller than `context`. The gateway refuses a
   file where it is not. Set `think: true` only on a tier that people choose by hand. A
   tier with `think: true` is never picked by automatic routing. It also needs a model
   that can reason, which its Ollama library page lists as `thinking`. Ollama refuses a
   request for reasoning from any other model, and `doctor` does not check this.
4. Restart the gateway and run `python dc.py doctor`. The "Model" line must say
   installed. Names are compared without regard to case, and a name with no tag means
   `:latest`.
5. A model that Ollama lists as a remote stub (a cloud model) is refused. The gateway
   will not use a model that is not on this computer.

To add a model for a provider other than Ollama, add its catalog under `catalog.<provider
name>`. For `org_server` the names are only labels, because the server maps tiers to its
own models. The context and output limits are still needed so the gateway can size
prompts.

To add a tier, the tier names are fixed in `TIER_ORDER` in `app/config/settings.py` and
the router, so a new tier is a code change and not only configuration. `specialist` is
reserved and disabled.
