# Deployment Guide: the Ubuntu Pilot

**Status: a plan, not a record.** Beta 1 runs on one workstation. No Ubuntu server
exists, and nothing in this document has been run on Ubuntu. Where a step is marked
**untested**, it is written from the documentation of the tools involved and has not
been tried with this project. Where something is marked **not built**, it does not
exist in the repository yet.

The beta is not approved for production use and claims no compliance with any
government framework. A pilot with real staff needs the items under "Before production"
in [security.md](security.md), several of which are listed below as not built.

## What exists and what does not

| Piece | State |
|---|---|
| Gateway bound to a loopback address, with a bearer token | Built and tested on Windows |
| Relay route: workstation gateway calls an organization model server | Built. Tested only against a second copy of the gateway standing in for the server |
| Stand-in model server (`inference_service.enabled`) | Built, for testing. It is not a production server |
| `shared_token` identity mode | Built. One token is one person, so it is interim |
| Ollama cloud gate | Built. The gateway refuses Ollama while cloud access is on |
| The organization's own model server | Does not exist |
| TLS in front of a gateway | Not built. Described below |
| Per-user organizational authentication, roles and groups | Not built |
| systemd unit, installer or package | Not built. An example unit is below, untested |
| Docker Compose | Not built. See the note at the end |
| PostgreSQL, Qdrant, document ingestion, local embeddings | Not built |
| Audit logging, encrypted storage, controlled backups | Not built |
| Anything tested on Ubuntu | None |

## Target

Ubuntu Server 24.04 LTS, on a machine with one or more NVIDIA GPUs, on a segmented
network. The gateway and Ollama bind to loopback on that machine. A TLS reverse proxy
is the only listener the network can reach. There are two ways to use it, matching the
two routes in [architecture.md](architecture.md):

- **Relay.** The server runs the model, and each workstation runs its own gateway,
  which calls the server. Conversations stay on the workstations. The server stores
  nothing. This is the supported route for several staff.
- **Direct.** The server also hosts the gateway, and terminals connect to it. All
  conversations live on the server under one identity until per-user authentication
  exists, so it is for a single tester.

## Base system

Start from a minimal install of Ubuntu Server 24.04 LTS, fully updated, with SSH keys
and no password login.

```
sudo apt update
sudo apt full-upgrade
sudo apt install python3 python3-venv git
```

Untested. Ubuntu 24.04 ships Python 3.12, which is the version the tests run on.

### NVIDIA drivers

Inference runs on CPU without a driver, so this step is for speed. Choose the driver
the administrator trusts for the card, install it, restart and check:

```
ubuntu-drivers devices
sudo ubuntu-drivers install
sudo reboot
nvidia-smi
```

Untested. `nvidia-smi` should list each GPU. If Secure Boot is on, the kernel module may
need to be enrolled before it loads. Ollama uses the GPU when it finds a working driver,
and falls back to CPU otherwise, which is slow. Check which one is in use with Ollama's
`ollama ps`, which reports where a loaded model is running. No NVIDIA hardware is assumed
anywhere in this project's code.

### Firewall

Allow SSH and the proxy's port, and nothing else. The gateway and Ollama do not need
rules, because they listen on loopback only.

```
sudo ufw allow OpenSSH
sudo ufw allow 443/tcp
sudo ufw enable
```

Untested. Allow SSH before enabling the firewall, or you can lock yourself out.

## Ollama

Install Ollama by the method its documentation gives for Linux, then pull the model
named in `config/models.yaml`. Two settings matter for this project, and both are set on
Ollama and not on Digital Commons.

| Setting | Why |
|---|---|
| `OLLAMA_NO_CLOUD=1` | Turns off Ollama cloud access, so a chat cannot be forwarded to a remote service. The gateway checks `GET /api/status` before every chat and refuses Ollama unless `cloud.disabled` is `true`. |
| `OLLAMA_HOST=127.0.0.1:11434` | Keeps Ollama on loopback. Model-server ports must not be reachable from the network. Check that no other value is set, because a host of `0.0.0.0` exposes it. |

With the standard systemd service, set them in an override:

```
sudo systemctl edit ollama
```

and put in the editor:

```
[Service]
Environment="OLLAMA_NO_CLOUD=1"
Environment="OLLAMA_HOST=127.0.0.1:11434"
```

then:

```
sudo systemctl daemon-reload
sudo systemctl restart ollama
curl -s http://127.0.0.1:11434/api/status
```

Untested on Ubuntu. The status reply should contain `"disabled":true`. The environment
variable is the right way here. The service runs as its own user, so
`python dc.py setup`, which edits `~/.ollama/server.json` for the user running it, does
not change what the service reads. The `OLLAMA_NO_CLOUD` switch itself was verified on
Windows with Ollama 0.40.13.

Confirm from outside the machine that port 11434 does not answer.

## The gateway

The gateway reads `config/` relative to the repository, so put the whole repository on
the server.

```
sudo useradd --system --home /var/lib/digital-commons --shell /usr/sbin/nologin digital-commons
sudo mkdir -p /opt/digital-commons /var/lib/digital-commons /etc/digital-commons
sudo git clone https://github.com/BedrotAudio/digital-commons.git /opt/digital-commons
sudo python3 -m venv /opt/digital-commons/backend/.venv
sudo /opt/digital-commons/backend/.venv/bin/python -m pip install -r /opt/digital-commons/backend/requirements.txt
sudo chown -R digital-commons: /var/lib/digital-commons
```

Untested. `requirements.txt` holds the runtime packages and `requirements-dev.txt` adds the
test tools. The Go terminal is not needed on the server.

The data folder is `/var/lib/digital-commons`, set with `DC_DATA_DIR`. The gateway makes it
readable by its own user only.

### Configuration

Put server settings in a file outside the repository and point `DC_CONFIG` at it. A `.local`
file next to it is also read. For example `/etc/digital-commons/gateway.yaml` is a copy of
`config/development.yaml` with the changes for the deployment: the provider, the model
catalog if the models differ, the allow-lists, and the identity.

Two rules apply:

- `gateway.host` must stay a loopback address. The gateway refuses to start otherwise, in
  every identity mode. This is deliberate, and it is why a proxy is needed.
- `security.allowed_client_hosts` must list the public name that the proxy forwards, as
  the gateway checks the `Host` header to block DNS rebinding.

For the direct route, change the identity too:

```yaml
identity:
  mode: shared_token
  token_env: DC_GATEWAY_TOKEN
```

and keep the secret in a file only root can read, `/etc/digital-commons/gateway.env`, mode
`0600`:

```
DC_GATEWAY_TOKEN=<at least 32 random characters>
```

`shared_token` mode refuses to start with a shorter token. The development identity in the
file (`local.developer`, with its groups and permissions) applies to everyone who holds
the token. Review it, because nothing yet separates users.

### Example systemd unit

Save as `/etc/systemd/system/digital-commons.service`. **Untested.** It is a starting point
for the administrator to review.

```
[Unit]
Description=Digital Commons gateway
After=network.target ollama.service

[Service]
Type=simple
User=digital-commons
Group=digital-commons
WorkingDirectory=/opt/digital-commons/backend
Environment=DC_DATA_DIR=/var/lib/digital-commons
Environment=DC_CONFIG=/etc/digital-commons/gateway.yaml
EnvironmentFile=/etc/digital-commons/gateway.env
ExecStart=/opt/digital-commons/backend/.venv/bin/python -m app
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/digital-commons

[Install]
WantedBy=multi-user.target
```

Then:

```
sudo systemctl daemon-reload
sudo systemctl enable --now digital-commons
curl -s http://127.0.0.1:8000/healthz
journalctl -u digital-commons -e
```

The gateway logs method, route, status, duration and request id, and never message text, so
the journal holds no conversations. It exits with code 2 and a one-line reason when its
configuration is refused.

## TLS reverse proxy

A proxy on the same machine terminates TLS and forwards to `127.0.0.1:8000`. **Not
built, and untested.** Any proxy works if it does these things:

- Present a certificate that the workstations trust. If it comes from an organization
  authority that workstations do not already trust, a relay gateway needs
  `providers.org_server.ca_bundle` to point at that authority's certificate. The terminal
  has no certificate setting of its own, so for the direct route the workstation's system
  trust store must already trust the certificate.
- Pass the original `Host` header unchanged.
- **Turn off response buffering and allow long waits.** Replies stream as server-sent events,
  the first token can take many minutes on a cold model, and the gateway sends keep-alive
  pings while it waits. The gateway already sends `X-Accel-Buffering: no`.
- Not add an `Origin` or `Sec-Fetch-Site` header, because the gateway refuses any request
  that carries either, and not rewrite the path.
- Limit the request body size, but not below the gateway's own cap, so that an oversized
  request gets the gateway's own refusal and not a proxy error page, which the terminal
  can only report as a general error. With the shipped `limits` the cap is about 6.5 MB,
  which leaves room for four 256 KB files after JSON escaping.

An nginx server block along those lines, with example names:

```
server {
    listen 443 ssl;
    server_name ai.example.gov;
    ssl_certificate     /etc/ssl/example/fullchain.pem;
    ssl_certificate_key /etc/ssl/example/privkey.pem;

    client_max_body_size 7m;

    location / {
        proxy_pass http://127.0.0.1:8000;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_buffering off;
        proxy_read_timeout 1800s;
        proxy_send_timeout 1800s;
    }
}
```

The gateway does not trust forwarded address headers (`proxy_headers` is off), so its logs
will not show client addresses. Add access logging at the proxy if that is wanted, and keep
message bodies out of it.

On the workstation, set `gateway.base_url` in `config/terminal.yaml` to
`https://ai.example.gov` and `DC_GATEWAY_TOKEN` to the token. A non-loopback gateway must
use `https`. The terminal then shows organization text and ignores any claim that
processing is local.

## The organization model server (relay)

The relay route needs a server that implements the model host contract in
[api.md](api.md). The organization may build that however it likes, in any FastAPI
application. The contract is two routes:

- `GET /healthz`, which returns 200 when the server can serve.
- `POST /v1/inference/stream`, with `Authorization: Bearer <service token>`. The body holds a
  `tier` and a list of messages. The server maps the tier to its own model, enforces its own
  limits, and answers with the server-sent event format in the API document, with
  keep-alive pings. Closing the connection cancels generation. It stores nothing.

For testing before that server exists, a copy of this gateway can serve the contract:

```yaml
inference_service:
  enabled: true
  token_env: DC_SERVICE_TOKEN
```

with `DC_SERVICE_TOKEN` set to at least 32 characters in its environment. It answers with its
own active provider (Ollama on a GPU machine, or the mock provider for tests), and stores
nothing from a relayed request. It is still a whole gateway, with its own conversation
database and chat routes, which a real model server should not have, so it is a stand-in
and not a design for production. It is also not hardened for a network. Put it behind the
proxy above.

Each workstation then points at the server. This is the same change as in the README:

```yaml
# config/development.local.yaml on the workstation
inference:
  default_provider: org_server
providers:
  org_server:
    base_url: https://ai.example.gov
security:
  allowed_upstream_hosts:
    - ai.example.gov
```

with `DC_ORG_SERVER_TOKEN` set in the environment of whoever starts the gateway. The gateway
refuses to start if the host is not listed, if the address is not `https`, or if the token is
not set. Workstations need no model and no Ollama.

How the organization distributes the service token to workstations is not designed. Today it
is an environment variable per person, which is not an acceptable answer for a pilot with
many staff.

## Docker Compose (Future)

Not built. There is no Dockerfile and no Compose file in the repository, and none of this
has been tried. A containerized deployment would need at least:

- A gateway container with no published port except through the proxy, and a read-only
  image, with the data folder on a named volume.
- Ollama in its own container, given GPU access through the NVIDIA Container Toolkit, on an
  internal network that no other host can reach. Its cloud setting would still have to be off.
- The proxy as the only container publishing a port.
- Secrets passed as Compose secrets or an env file with restricted permissions, never in the
  image or the repository.

These are intentions to design against, not instructions.

## Not covered here

Backups and their retention, an audit trail, encryption of the conversation database, user
accounts and groups, session expiry, monitoring, updating the model and the software, and
independent security testing. They are on the list in [security.md](security.md) and in
[roadmap.md](roadmap.md), and each needs a decision by the organization.
