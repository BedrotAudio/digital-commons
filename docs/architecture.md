# Digital Commons Architecture

Status: Beta 1 design. *Future* marks anything not part of Beta 1.

**One AI workspace, two user experiences, one secure infrastructure.**

## System overview

```
┌────────────────────────────┐
│ Digital Commons Terminal   │  Go, Bubble Tea v2. Single binary.
│  Basic mode | Advanced mode│  Holds no inference logic and no provider URLs.
└─────────────┬──────────────┘
              │ HTTP + Server-Sent Events (docs/api.md)
┌─────────────▼──────────────┐
│ Gateway (FastAPI)          │  The only component that talks to model servers.
│                            │
│  Identity and permissions  │  Decides what this user may do.
│  Model router              │  Chooses a tier, inside what policy allows.
│  Model registry            │  Maps tiers to models for the active provider.
│  Document retriever        │  Filters by permission before any text is used.
│  Conversation store        │  SQLite now, PostgreSQL later.
│  Provider adapters ────────┼──┐
└────────────────────────────┘  │ exactly one active provider per gateway
        ┌───────────────┬───────┘
        ▼               ▼               ▼
     ollama            mock        inference_api
  (local models)   (demo, no      (organization FastAPI
                     model)        model server)
```

The two modes are presentations of the same session. Mode lives in the terminal
and permissions live in the gateway. Changing mode changes what is displayed,
never what the user may do. Documents are filtered by identity, so mode cannot
appear in any authorization decision.

Adding a provider means implementing the `InferenceProvider` protocol. llama.cpp
is expected to arrive this way, and is not part of Beta 1.

## Switching to the organization model server

The first version talks to a local model. Moving to an organization-hosted server
is a configuration change, with no code change on either side:

| Route | What changes | Conversations live |
|---|---|---|
| **Relay**: terminal to local gateway to organization server | In the gateway config: `inference.default_provider: org_server`, `providers.org_server.base_url`, add its host to `security.allowed_upstream_hosts`, and export `DC_ORG_SERVER_TOKEN`. | On each workstation. |
| **Direct**: terminal to a gateway hosted by the organization | `gateway.base_url` in the terminal config, plus `DC_GATEWAY_TOKEN`. | On the server, under one identity. |

**Relay is the supported route for several staff in Beta 1.** The local gateway
keeps conversations, policy, documents and context assembly, and calls one
stateless endpoint on the organization server (`POST /v1/inference/stream`, see
`docs/api.md`). The server stores nothing. Any FastAPI server implementing that
one endpoint qualifies, so the organization is free to build it however it likes.
No organization server exists yet, so a second copy of this gateway running the
mock provider stands in for it during testing.

**Direct is for a single tester until per-user authentication exists**, because
one shared token means one identity and one chat history. Per-user authentication
is *Future*.

A conversation belongs to the locality it began under. If the active provider
changes, an old conversation refuses further turns rather than quietly moving
text written under one indicator to another place.

## Request flow

1. The terminal posts one user turn to `POST /v1/chat/stream`. History is held by
   the gateway, not resent by the client.
2. The gateway authenticates the caller and computes the permitted tiers.
3. The router picks a tier from those permitted tiers. Policy runs first, so the
   router cannot widen access.
4. The registry resolves the tier to an installed model on the active provider.
   If that model is missing it falls back to another tier on the same provider and
   says so. It never falls back across providers or to a cloud service.
5. If documents were requested, the retriever filters candidates by the caller's
   groups, then ranks only those.
6. Context is assembled within the tier's token budget.
7. The turn starts in its own task, which owns the inference slot and writes the
   result. The response only drains that task's event queue, so keep-alive pings
   flow during a long wait and a disconnect cancels cleanly.

Everything through step 6 runs before the response begins, so `start` reports a
finished plan and early failures are ordinary errors. See "Turn lifecycle" in
`docs/api.md` for exactly what is written and when.

## Measured behaviour that shaped the design

Measured on 2026-11-01 on an Intel Core Ultra 7 258V with 32 GB RAM, no NVIDIA
GPU, Ollama 0.40.13 and `Gemma4:12b`. Inference ran entirely on CPU.

| Observation | Design consequence |
|---|---|
| With no `think` key, Ollama enables reasoning. A 120-token limit was spent entirely on reasoning and produced no answer. | The adapter always sends `think` explicitly, from per-tier config. |
| `think: false` gave a first token in 2.2 s and a complete short answer in 8 s. | Fast and Standard default to `think: false`. The router never picks a reasoning tier; it is a manual choice. |
| A cold load took about 200 s to the first token. | Background warm-up, a long `keep_alive`, and a plain-language loading state. |
| Generation runs at about 4 to 6 tokens per second, prompt reading at about 17. | A 900 s first-token limit, gateway pings, one inference at a time. |
| Ollama reloads a model when `num_ctx` changes. | Tiers sharing a model must share `context`. Config validation warns otherwise. |
| Exceeding the context silently discards the start of the prompt. | The adapter sends `truncate: false` and the gateway manages budget itself. |
| Ollama sends no HTTP headers until the first token. | The idle timeout applies only after the first chunk. |
| Ollama can forward a model to a remote service, even from a loopback URL. | The gateway checks Ollama's own cloud status before every request. See `docs/security.md`. |

## Model routing

`RuleBasedRouter` scores simple, explainable signals such as prompt length, code
markers and complexity vocabulary. It is a transparent placeholder and is not
claimed to be intelligent. `Router` is a protocol, so a local classifier,
resource-aware policy or administrative rules can replace it without touching
callers. A decision always carries a reason, so Advanced mode can display it.

## Deliberate changes from the brief

| Brief | Built | Why |
|---|---|---|
| Bubble Tea, Lip Gloss, Bubbles | The v2 releases (`charm.land/*/v2`) | v2 is the current stable line. Windows Shift+Enter still requires the app to enable win32-input mode itself, which it does. |
| `make` commands | `dc.py` task runner, with a thin `Makefile` over it | `make` is not on this Windows machine and Python is already required, so one runner works on every platform. |
| Each tier names a provider | One active provider per gateway | Mixing providers would let automatic routing silently move a conversation from this computer to a server. One provider keeps the locality indicator truthful. |
| Phase 1 only | Phase 1, plus history, routing, Markdown, highlighting and usage figures from Phase 2 | Basic mode's required list and the "polished beta" goal need them. |
| Placeholder search | Small labelled sample collection with keyword search and server-side permission filtering, reached through chat. Each sample says `sample: true` in its front matter, so its citations carry `sample` and the model sees it marked as invented. | Proves citations and the permission boundary without building ingestion. |
| PostgreSQL later | SQLite behind a `ConversationStore` protocol | No server required today, with a one-class swap later. |
| One combined config file | `config/terminal.yaml` separate from gateway config | The terminal is deployed independently and must not hold provider URLs. |
| Audit service box | Not in Beta 1 | Audit is on the Future production list, and storing less suits a local beta. The gateway logs metadata only. |
| llama.cpp adapter stub | Provider protocol only | The brief does not require it yet, and an unused adapter is untested code. |

## Supported platforms

Windows and macOS are the two supported workstation platforms. Ubuntu is the future
organization server. The same code serves all of them: one Go terminal and one
Python gateway, with a few differences kept in small, tested places.

| | Windows | macOS |
|---|---|---|
| Terminals | Windows Terminal, conhost | Terminal.app, iTerm2, Ghostty, kitty, WezTerm, VS Code |
| Data folder | `%LOCALAPPDATA%\DigitalCommons` | `~/Library/Application Support/DigitalCommons` |
| Refused data folders | OneDrive | iCloud Drive, `~/Library/CloudStorage`, `~/Desktop`, `~/Documents` |
| Python command | `python` | `python3`, because macOS has no `python` |
| Model speed | CPU here, so replies are slow | Apple Silicon uses its GPU through Ollama, so replies are much faster |
| Distribution | Unsigned today, and antivirus may object | Built from source today. A shared binary needs Developer ID signing and notarization. |

Linux works as a side effect, and is what the organization server will use.

## Repository layout

```
terminal/     Go client. No model-specific logic.
backend/      FastAPI gateway.
config/       Gateway and terminal configuration.
data/         Sample documents only. Runtime data lives outside the repository.
docs/         Requirements, architecture, API, UI, security and operations.
tests/        Backend and end-to-end tests. Go tests sit beside their code.
scripts/      Shell wrappers over dc.py.
dc.py         Cross-platform task runner.
```
