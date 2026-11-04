# Digital Commons Requirements

Condensed from the project brief. Every item here is a requirement unless marked
*Future*. Where this and another document disagree, raise it rather than guess.

## Owner's priorities

These notes from the project owner override everything else below.

1. Basic mode is the default experience.
2. Do not overbuild the first version.
3. The most important thing is a polished Basic mode and Advanced mode, both
   talking to a local model.
4. It must switch modularly, through FastAPI, to the organization's model hosting
   server. That server does not exist yet.
5. Qdrant, PostgreSQL, document ingestion and multi-server routing come later.
6. The code must be portable.
7. Basic mode must feel familiar to ChatGPT users, because that is what they know.
8. There is no Ubuntu server yet.
9. This is a serious beta, not a throwaway prototype.
10. Digital Commons runs on Windows and macOS. Both are equal, supported workstation
    platforms, on Apple Silicon and Intel Macs. Everything a person touches on one
    must work on the other, including install, startup, keys, and every message that
    names a command or a folder. Ubuntu is the future organization server, not a
    workstation target.

## Product

A secure, locally operated AI workspace for public employees, for research,
writing, document analysis, problem-solving and software development.
Design principle: one AI workspace, two user experiences, one secure
infrastructure.

## Basic mode (default)

Polished professional government application, not a developer terminal.
No knowledge of AI, models or infrastructure required. Provides: conversation
interface, welcoming home screen with suggested tasks, natural-language Q&A,
document summarization and comparison, writing help, research help, explanations
of policies and procedures, source citations when documents are used,
conversation history, clear controls to start and end conversations, and a
simple indicator of whether the AI is operating locally.

Suggested tasks: summarize a document, compare two policy documents, help draft
correspondence, explain a regulation in plain language, find information in
approved documents, analyze a spreadsheet, brainstorm solutions.

Must hide model names, GPU use, context settings, inference parameters and
backend configuration unless the user explicitly opens an information panel.

## Advanced / Developer mode

Full conversation, manual model selection, automatic routing, model and inference
status, coding assistant mode, technical document analysis, Markdown rendering,
code blocks with syntax highlighting, copy code, streaming, conversation and
context controls, model configuration visibility, token usage and timing where
available, backend connectivity status, diagnostics appropriate to permissions.
*Future*: isolated code execution.

Commands: `/help`, `/mode basic`, `/mode developer`, `/model auto`, `/model fast`,
`/model standard`, `/model advanced`, `/code`, `/search`, `/status`, `/privacy`,
`/clear`, `/exit`.

No shell execution or host access through the assistant. Advanced mode is an
expanded interface, not an exemption from security controls.

## Mode switching

Users can switch both ways. Basic is the default. Advanced is explicitly enabled
and may later be restricted by organizational permissions. Both modes share the
same backend, authentication architecture, conversation infrastructure and
security controls. Changing mode must not bypass authorization or grant access.

## Visual identity

Government and enterprise: trust, professionalism, stability, privacy,
institutional credibility, accessibility, technical capability. Avoid gaming or
hacker aesthetics, neon and overly futuristic design. Dark navy or charcoal, with
light mode also available. Muted green and slate accents. Clear type, subtle
borders, excellent readability, minimal clutter, labeled actions, consistent
spacing, accessible contrast. Full-screen layout with header, collapsible
sidebar, main workspace, multiline input, footer. Must adapt to terminal size and
must not assume a large window.

## Technology

Terminal: Go, Bubble Tea, Lip Gloss, Bubbles, Cobra, standard library HTTP and
JSON. Independently deployable. No model-specific inference logic.

Gateway: Python, FastAPI. Responsible, over time, for model routing, inference,
authentication, authorization, document retrieval, audit events, conversation
management, model availability and usage statistics.

Inference: Ollama now, including availability checks and streaming. llama.cpp
later through an extensible adapter, not required for the first version.
No NVIDIA assumption. CPU-only must work.

*Future*: Ubuntu Server 24.04 LTS, NVIDIA GPUs, Docker Compose, Qdrant, PostgreSQL,
local embeddings, dedicated model servers, a Mixture of Experts server.

## Architecture

The terminal talks to the gateway over a defined HTTP API. It must never contact
Ollama, Qdrant, PostgreSQL or model servers directly. The gateway abstracts the
inference provider. Tiers: Fast, Standard, Advanced, and Specialist (*Future*).
Automatic routing and manual selection in Advanced, automatic in Basic. Simple
rules are acceptable if not presented as production-grade intelligence. The
router is replaceable and must never override authorization. Never escalate to an
external cloud model if a local model fails.

## Development

One computer: install Go, Python and Ollama, get a small model, start the gateway,
start the terminal, have a complete conversation. Simple commands for setup,
backend, frontend, dev, test and clean. Configurable local gateway URL. Detect
Ollama being unavailable and explain it clearly. Mock/demo mode that needs no
model. No external API keys. No cloud fallback. Model names live in configuration.
Do not assume three models can be loaded at once.

## Privacy and security

Core requirements, not later additions:

1. All model inference is local.
2. No external AI APIs.
3. No automatic cloud fallback.
4. No telemetry or analytics to third parties.
5. Local services bind to loopback by default.
6. Do not expose model-server ports to the LAN.
7. No hardcoded credentials or secrets.
8. Do not log complete prompts or responses by default.
9. Avoid retaining sensitive conversation content unnecessarily.
10. Provide a visible privacy status panel.
11. Clearly distinguish local processing from any external connectivity.
12. Do not give the terminal direct access to privileged infrastructure.
13. Do not let users supply arbitrary model-server URLs.
14. Do not execute model-generated shell commands automatically.

Clearly label prototype-only behaviour. Never claim production security or
compliance with any government framework. Local inference alone does not make an
application secure or compliant. *Future* production needs: organizational
authentication, role and group authorization, TLS, department-aware document
access, server-side permission filtering, encrypted storage, controlled backups,
audit logging, session expiry, model allowlists, network segmentation, security
testing and approved retention policies.

## Documents

Design for later RAG without redesigning the terminal: approved documents,
parsing and OCR, chunking, local embeddings, Qdrant, permission-filtered
retrieval, response with citations. Metadata: document id, department,
classification, allowed groups, version, source filename, page number, document
date. Permissions are enforced on the server before content reaches a model.
Never rely on the model to decide authorization. First version: a small
demonstration collection is sufficient.

## Acceptance criteria

1. Launches in a terminal.
2. Basic mode works.
3. Advanced mode works.
4. Users can switch between modes.
5. Interface remains usable at different terminal sizes.
6. A user can send a message to a local Ollama model.
7. The model streams a response.
8. An unavailable model is handled gracefully.
9. Mock/demo mode works.
10. No cloud AI API is required.
11. The terminal does not connect directly to model providers.
12. Configuration is externalized.
13. Installable and launchable from documented instructions.
14. The codebase is organized and testable.

Provide automated tests for important behaviour. Never claim something works
unless it has been tested. Clearly identify incomplete or simulated functionality.

## Final deliverables

Summary of what was built, repository structure, exact install and startup
instructions, how to download and configure a local model, how to test Basic and
Advanced modes, how to run automated tests, known limitations, and recommended
next steps toward the Ubuntu pilot.
