# Roadmap

Where the project stands and what comes next. The owner's priorities in
[requirements.md](requirements.md) govern the order: a polished Basic mode and Advanced
mode talking to a local model come first, and Qdrant, PostgreSQL, document ingestion and
multi-server routing come later. Do not build ahead of them.

## Phases

[architecture.md](architecture.md) refers to a Phase 1 and a Phase 2 of the original brief.
The brief itself is not in the repository, so this page names the later stages by what they
are and does not number them.

| Stage | Contents | State |
|---|---|---|
| Phase 1 | A terminal and a gateway that hold a streamed conversation with a local model, Basic and Advanced modes, mode switching, mock mode, configuration outside the code | Done in Beta 1 |
| Phase 2 (part) | Conversation history, automatic routing, Markdown and syntax highlighting, token and timing figures | Done in Beta 1, because Basic mode's required list and a polished beta need them |
| Ubuntu pilot | An organization model server, organizational sign-in, TLS, a pilot group of real staff | Next. See below |
| After the pilot | Document ingestion with Qdrant, PostgreSQL, local embeddings, a Specialist tier, more than one model server, isolated code execution | Later |

## What Beta 1 delivered

The terminal:

- Basic mode as the default, with a home screen of suggested tasks, a conversation view, a
  sidebar of past chats, a composer with file attachment, and a plain-language privacy
  indicator.
- Advanced mode: a tier picker and automatic routing, a coding assistant, highlighted and
  numbered code blocks with copy, an inspector with token, timing and context figures,
  and full status.
- Switching both ways without changing what a person may do.
- Dark and light themes, a layout that adapts down to a 40-column window, mouse and keyboard
  use, and a sanitizer for everything the gateway sends before it is displayed.

The gateway:

- Providers for Ollama, a demonstration mode that needs no model, and an inference API for
  an organization server, behind the `InferenceProvider` protocol.
- A gate that refuses Ollama while its cloud access is on, locality worked out in code and
  not configuration, and conversations that stay in the place they began.
- A bearer token on every route, loopback-only binding, protection against web pages and DNS
  rebinding, and logs that never hold message text.
- Conversations in SQLite with secure deletion and retention, a small collection of invented
  sample documents with permission filtering before ranking, and citations.
- Warm-up of the model at start, one request at a time, and cancellation.

The tooling:

- `dc.py` for setup, running, testing, cleaning and diagnosing on Windows and macOS, with
  wrappers for `make`, Windows and shell users, and a guided step to turn off Ollama cloud
  access.
- Automated tests of the gateway, of `dc.py` and of the terminal, including an end to end
  test that drives the real terminal in a pseudo terminal. A workflow in
  `.github/workflows/tests.yml` is set up to run them on Windows, Ubuntu and Apple Silicon
  and Intel Macs, with a job that checks the cloud gate against a real Ollama on a Mac:
  chat is refused while cloud access is on, and works once `setup` has turned it off.

What Beta 1 has not shown:

- No person has used it at a real Mac. GitHub's Mac machines (Apple Silicon and
  Intel) pass every automated test and the cloud gate check with a real small model,
  but they have no screen, keyboard or permission dialogs.
  [mac-checklist.md](mac-checklist.md) lists what needs a person at a real Mac.
- Linux is tested only on GitHub's Ubuntu machine, where every automated test passes.
- There has been no review by a security tester.
- A few interactions need a person at a keyboard: Shift+Enter in Windows Terminal, paste from
  the real clipboard, and copying through the terminal's clipboard escape sequence.

## Next: toward the Ubuntu pilot

In order. Each step should leave the beta working.

1. **Use it at a real Mac.** GitHub's Apple Silicon and Intel Mac machines already pass
   every automated test. What remains needs a person: follow the README's macOS steps,
   including Homebrew, then work through [mac-checklist.md](mac-checklist.md) in the
   macOS terminals named in [architecture.md](architecture.md), and time replies with a
   real model on Apple Silicon. This is a requirement of the owner, not an extra.
2. **Run a long session with the real model.** On both platforms, hold a long conversation
   with `Gemma4:12b` or its replacement, with attachments and document search, and record
   timings. Settle the tier defaults and the router's thresholds from what is measured.
3. **Build the organization model server.** A FastAPI service that implements the model host
   contract in [api.md](api.md), on Ubuntu Server 24.04 LTS, in front of Ollama or llama.cpp
   on the NVIDIA machine. Keep it stateless. Test the relay route against it with a few real
   workstations. See [deployment.md](deployment.md).
4. **Put TLS in front.** A reverse proxy with an organization certificate, and a way for
   workstations to trust it. Decide how a service token reaches a workstation without being
   typed.
5. **Organizational authentication.** Replace the shared token with real per-person identity,
   take groups and permissions from the directory and not from a file, and add session
   expiry. This unlocks the direct route for more than one tester. Beta 1 has the token
   check and one configured user, and no sign-in.
6. **Real documents.** Ingestion, parsing (including PDF and Word, which attachments cannot
   carry today) and OCR, chunking, local embeddings and Qdrant, with the same rule as the
   sample collection: filter by permission on the server before anything is ranked or
   shown to a model.
7. **Move storage to PostgreSQL** behind `ConversationStore` when conversations live on a
   server. Add encryption at rest and controlled backups at the same time.
8. **Audit events and retention policy.** Audit is deliberately absent from Beta 1 because
   storing less suits a local beta. A pilot needs it, and needs a retention period that the
   organization has approved.
9. **Package for people who are not developers.** A signed Windows binary and a signed,
   notarized macOS binary so that nobody builds from source, with an installer or a script
   that sets Ollama up. Signing also removes most of the trouble with endpoint protection
   software described in the README.
10. **Independent security testing** before real staff data is used.

## Later

- A llama.cpp provider, added through the `InferenceProvider` protocol.
- A Specialist tier backed by a Mixture of Experts server.
- Routing across more than one model server, with a router that is smarter than the current
  rules. The router stays replaceable and must never override authorization.
- Isolated code execution for Advanced mode. Until then the assistant runs nothing.
- Docker Compose for the server side.

## Not planned

A cloud fallback of any kind. Nothing in the roadmap sends a conversation to an outside AI
service.
