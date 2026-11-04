# Digital Commons Security Design

This beta is not approved for production use and makes no claim of compliance
with any government security framework. Local inference alone does not make an
application secure or compliant. This document says what is enforced, what is
accepted for the beta, and what must exist before production.

## Threat model for Beta 1

Protected against:

- Prompts leaving the machine through Ollama cloud features, a system proxy or an
  upstream redirect.
- Web pages in a browser on the same computer reaching the gateway.
- Another Windows or Linux account on the same computer reading conversations
  through the gateway.
- Hostile text in a model reply, attachment or document taking over the terminal,
  or being used to widen access.
- Message content reaching logs.

Accepted for the beta and stated plainly:

- Malware running as the same user can read the data folder and the local token.
- Another process listening on port 8000 while the gateway is down is not detected.
- Conversations are not encrypted at rest. The privacy panel always says so.
- Deleted data can survive on SSDs and in machine backups. The gateway removes it
  from the database and the write-ahead log, and cannot speak for the disk.
- Ollama itself may contact ollama.com to check for updates. The privacy panel
  says so. No prompt content is involved.

## Inference stays local

### Ollama cloud access is a gate, not a check on names

Ollama can forward a chat to a remote service. A stub can be created under the
exact configured model name, and any name ending in `:cloud` or `-cloud` is
forwarded with nothing installed. Name matching therefore proves nothing and is
not used as a control. Neither is a check made only at startup, because a model
name can be re-pointed afterwards.

1. **The gate.** The Ollama adapter calls `GET /api/status` at startup and before
   every chat. `cloud.disabled` must be exactly `true`. A `false` value, a non-200
   reply, a 404 or a malformed reply fails closed: chat returns
   `model_not_permitted`, `/v1/status` reports `unavailable` with reason
   `cloud_access_enabled`, and locality is never `this_device`.
2. **Turning it off.** `python dc.py setup` offers to merge
   `{"disable_ollama_cloud": true}` into `~/.ollama/server.json`, after showing the
   change. Ollama must then be restarted. Setting `OLLAMA_NO_CLOUD=1` has the same
   effect. A gateway cannot set an environment variable for a service it did not
   start, so this is an explicit step.
3. **Defense in depth.** At startup and warm-up, reject a configured model whose
   `/api/show` reply has a non-empty `remote_host` or `remote_model`. Abort a stream
   and raise an error if any chunk carries those fields. This detects a leak after
   the fact and does not prevent one, which is why the gate comes first.

### Proxies and redirects

Every upstream client comes from one factory:

```python
httpx.AsyncClient(trust_env=False, follow_redirects=False, verify=context)
```

`trust_env=False` matters because httpx would otherwise send prompts for
`127.0.0.1` through `HTTP_PROXY` or a Windows system proxy. A 3xx from any upstream
becomes `provider_error`. `verify=False` is never allowed. The optional `ca_bundle`
provider setting exists for organization certificates.

### Locality is computed

No configuration file states where processing happens. One function decides:

| Result | Condition |
|---|---|
| `demo` | Provider kind is `mock`. |
| `this_device` | Provider kind is `ollama`, its `base_url` host parses as loopback, and the cloud gate passes. |
| `organization` | Everything else. |

Config validation refuses a non-loopback `base_url` for `ollama`. Only
`inference_api` may be remote, and only over HTTPS to a host listed in
`security.allowed_upstream_hosts`. That list is a guard against misconfiguration,
not a barrier against someone who can edit configuration, so the real upstream
host is also shown in the privacy panel.

Loopback checks parse the host and never compare prefixes. `127.0.0.1.evil.gov`,
`localhost.evil.gov`, `127.0.0.1@evil.gov`, `0.0.0.0`, `127.1` and `2130706433` must
all be rejected.

The terminal does not rely on the gateway alone. If its `gateway.base_url` is not
loopback, it ignores any `this_device` claim and shows fixed organization text.

### Conversations do not change location

Each conversation records its locality when created. A turn or regenerate whose
conversation locality differs from the active provider returns
`conversation_locality_changed` before any upstream call, and the terminal offers a
new chat. `/v1/status` reports locality on every poll, so the indicator updates
without a restart.

## Who can reach the gateway

- **Bind address.** Beta 1 refuses any non-loopback bind in every identity mode.
  This is enforced in the entry point that calls uvicorn, so a command-line host
  cannot skip it. An organization deployment puts a TLS proxy in front of a
  loopback-bound gateway and lists its name in `security.allowed_client_hosts`.
- **Token.** Every `/v1/*` route needs a bearer token, checked before the body is
  read and compared with `secrets.compare_digest`.
  - `local` mode: on first start the gateway generates a 256-bit token at
    `<data_dir>/local-token`. The terminal reads that file only when its gateway is
    loopback, and never sends it elsewhere.
  - `shared_token` mode: read from the variable named by `identity.token_env`.
    Startup is refused if it is unset or under 32 characters.
- **Web pages.** Requests carrying `Origin` or `Sec-Fetch-Site` are refused with 403.
  `Content-Type: application/json` is required on `POST`. The `Host` allowlist blocks
  DNS rebinding. `CORSMiddleware` is never added, and a test pins that.
- **Request size.** A body-size cap in middleware applies before parsing.
- **Forged client addresses.** uvicorn runs with `proxy_headers=False`, and
  `X-Request-ID` is always generated by the gateway.
- **API documentation routes.** `docs_url`, `redoc_url` and `openapi_url` are `None`
  unless `gateway.docs_enabled` is set.

## Authorization

- Mode is never an input to a decision.
- `advanced_mode` is enforced by the gateway: `assistant: code` requires it, and
  config loading rejects `manual_model_selection` or `view_diagnostics` without it.
- `allowed_tiers` is applied before routing, and the router receives only that list.
  A manual tier outside it returns `permission_denied`.
- `route.model`, `route.provider` and `route.reason` are sent only with
  `view_diagnostics`.
- Every `ConversationStore` method takes `user_id` as a required argument.
  Identifiers are 128-bit random values.
- Document filtering happens before ranking. Hidden documents appear in no result
  and no count.
- The model never decides authorization.

## Prompt injection

Attachment and document text is untrusted. It can shape a reply, and it cannot
widen access, as long as these hold:

- The adapter never sends `tools`, `format`, `raw` or `template`, and reads only
  `message.content`, `message.thinking` and the final statistics.
- Identity comes only from the token.
- Retrieval filters before ranking.
- Attachment and document text goes in a delimited user-role block, never in the
  system message. Delimiters are a convention and not a security boundary, so the
  points above carry the guarantee.

Reasoning and retrieved passages are never stored or resent.

## Terminal

- One `Clean` function runs on every gateway string before display: delta text,
  thinking, titles, attachment names, citations, notices, errors, privacy statements,
  suggestions and display names. It removes escape sequences, then drops control
  characters except newline and tab, including C1 controls, bidi controls
  U+202A to U+202E and U+2066 to U+2069, U+200B, U+2060, U+FEFF and tag characters
  U+E0000 to U+E007F.
- Streamed replies are cleaned as accumulated text on each render, because a sequence
  can straddle two deltas. Markdown and highlighting run afterwards, and Copy uses
  the cleaned text.
- OSC 8 hyperlinks are never emitted. A link renders as `text (full url)`, unshortened,
  because a hidden link target is the one way injected text could carry document
  content off the machine. Images render as plain text.
- The Sources block is built only from the `citations` event, never from model text.
  The reply's `[n]` citations can only narrow it to the sources they name.
- The HTTP client refuses redirects, because Go's default client would re-send a
  prompt to the redirect target. Any 3xx is treated as a connection failure.
- `TEA_TRACE` and `TEA_DEBUG` are unset before the program starts. Bubble Tea would
  otherwise log all input and output, including conversations, to a file.
- Nothing the model produces is ever executed.

## Data at rest

- All runtime data lives in one folder: `%LOCALAPPDATA%\DigitalCommons` on Windows,
  `~/Library/Application Support/DigitalCommons` on macOS, and
  `$XDG_STATE_HOME/digital-commons` (default `~/.local/state/digital-commons`) on
  Linux. `DC_DATA_DIR` overrides it. Relative paths are rejected.
- The gateway refuses to start if that folder would sync off the computer, because a
  synced database would quietly leave the machine. That means OneDrive anywhere, and
  on macOS iCloud Drive, `~/Library/CloudStorage` (OneDrive, Dropbox, Google Drive and
  Box) and `~/Desktop` and `~/Documents`, which iCloud can sync without changing the
  path.
- Time Machine backs up the macOS data folder unless an administrator excludes it.
- On Linux and macOS the gateway sets `umask 077` and creates the folder as 0700. On
  Windows it relies on the inherited ACL, since `chmod` does not change an ACL. That
  ACL is user-only inside the user profile and `%LOCALAPPDATA%`, which includes the
  default folder. A `DC_DATA_DIR` elsewhere, such as directly under `C:\`, usually
  inherits read access for every account. The gateway does not read ACLs, so for such
  a folder the `conversation_storage` privacy statement is `warn` and says other
  accounts may be able to open it, instead of calling it private.
- SQLite runs with `journal_mode=WAL`, `secure_delete=ON` and `temp_store=MEMORY` on
  every connection. After a user deletion and after each retention purge it runs
  `VACUUM`, then `PRAGMA wal_checkpoint(TRUNCATE)`. `secure_delete` alone is not enough:
  b-tree rebalancing leaves old copies of moved rows in page gaps, and a test found
  deleted text there. Retention runs at startup and hourly. A reply replaced by
  regenerate, or attached file text replaced by the shorter text a retry sent, leaves
  the database at once, but its text can stay in the file until the next deletion or
  purge runs `VACUUM`. Only deletion guarantees removal from the files.
- Attached file text is stored with its message, as far as it was sent to the model,
  so later questions in the same chat can use it while it still fits the budget. It is
  deleted with the message. When history trimming drops the turn that carried a file,
  an `attachment_left_out` notice names the file. The `conversation_storage` privacy
  statement says file text is stored, in Basic mode too.
- Only `number`, `document_id`, `section` and `version` of a citation are stored.
  Text is rebuilt on read through the permission filter.
- The notices sent with a reply, such as how much of a file was read, are stored with
  that reply and deleted with it, so a reopened chat shows them again.

## Logging

The gateway logs method, route template, status, duration and request id. It never
logs message text, titles, file names or exception text from user input.

- Request models set `hide_input_in_errors=True`.
- Validation failures log only `loc` and `type`, using `errors(include_input=False)`.
- uvicorn's access log is off, because the gateway's own request log replaces it.
- An end-to-end test places a canary in a message, a file name and a title and
  asserts it appears in no log output.

## Before production

Organizational authentication, per-user identity, role and group authorization,
TLS, department-aware document access at scale, encrypted storage, controlled
backups, audit logging, session expiry, administrative model allowlists, network
segmentation, code-signed binaries, independent security testing and approved
retention policies.
