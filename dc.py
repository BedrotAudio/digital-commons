#!/usr/bin/env python3
"""Digital Commons task runner.

Standard library only, so it runs before anything is installed. Works the same
on Windows, macOS and Linux. Run `python dc.py --help` for the commands (`python3` on
macOS and Linux).
"""

from __future__ import annotations

import os
import shutil
import sys

# macOS ships an old python3, so everything down to the version check has to run on
# one, and the whole file has to parse on one. tests/tooling/test_old_python.py checks.
MIN_PYTHON = (3, 11)
NEWER_PYTHONS = ("python3.12", "python3.13", "python3.11")
PYTHON_SWITCHED = "DC_PYTHON_SWITCHED"


def python_install_instruction(platform):
    if platform == "win32":
        return "winget install Python.Python.3.12"
    if platform == "darwin":
        return "brew install python@3.12"
    return "sudo apt install python3.12 python3.12-venv"


def python_too_old_message(version, platform):
    return (
        "Digital Commons needs Python {}.{} or newer. This is Python {}.{}.\n\n"
        "Install a newer one with:\n\n  {}\n\n"
        "Then open a new terminal window and run this command again."
    ).format(MIN_PYTHON[0], MIN_PYTHON[1], version[0], version[1], python_install_instruction(platform))


def newer_python(root, which=shutil.which):
    """Find a suitable interpreter when the one running this is too old.

    `brew install python@3.12` adds python3.12 and leaves python3 on the old one, so
    the python3 commands this tool prints would otherwise stop at the check below.
    """
    venv = os.path.join(root, "backend", ".venv", "bin", "python")
    if os.path.isfile(venv):
        return venv
    for name in NEWER_PYTHONS:
        found = which(name)
        if found:
            return found
    return None


if sys.version_info[:2] < MIN_PYTHON:
    better = None
    if os.name != "nt" and PYTHON_SWITCHED not in os.environ:
        better = newer_python(os.path.dirname(os.path.abspath(__file__)))
    if better:
        os.environ[PYTHON_SWITCHED] = "1"
        try:
            os.execv(better, [better] + sys.argv)
        except OSError:
            pass
    sys.exit(python_too_old_message(sys.version_info, sys.platform))
os.environ.pop(PYTHON_SWITCHED, None)

import argparse
import contextlib
import json
import re
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path, PurePath, PurePosixPath, PureWindowsPath
from typing import Callable, Iterable, Iterator, Mapping, Optional

ROOT = Path(__file__).resolve().parent
BACKEND = ROOT / "backend"
TERMINAL = ROOT / "terminal"
VENV = BACKEND / ".venv"
REQUIREMENTS = BACKEND / "requirements-dev.txt"
BIN = ROOT / "bin"
TERMINAL_CONFIG = ROOT / "config" / "terminal.yaml"

WINDOWS = os.name == "nt"
BINARY = BIN / ("digital-commons.exe" if WINDOWS else "digital-commons")

GATEWAY_WAIT_S = 30.0
GATEWAY_STOP_S = 10.0
TERMINAL_GRACE_S = 5.0

CLOUD_KEY = "disable_ollama_cloud"
LIFELINE_VARIABLE = "DC_LIFELINE_FD"
DIST = ROOT / "dist"
CROSS_TARGETS = (
    ("macOS arm64", "darwin", "arm64", "digital-commons-macos-arm64"),
    ("macOS amd64", "darwin", "amd64", "digital-commons-macos-amd64"),
    ("Windows amd64", "windows", "amd64", "digital-commons-windows-amd64.exe"),
)
# The private Python an installer ships, in place of backend/.venv.
RUNTIME = ROOT / "runtime"

# Where `start` gets Ollama when it is missing, and the publisher the download must be
# signed by, as each platform reports the signer on Ollama's own releases. On Windows
# that is Ollama Inc. On macOS it is the Apple Developer ID team below, which Apple
# lists under Ollama's earlier company name, Infra Technologies, Inc.
OLLAMA_DOWNLOADS = {
    "win32": ("https://ollama.com/download/OllamaSetup.exe", "OllamaSetup.exe", "about 1.7 GB"),
    "darwin": ("https://ollama.com/download/Ollama-darwin.zip", "Ollama-darwin.zip", "about 230 MB"),
}
OLLAMA_WINDOWS_SIGNER = ("CN=Ollama Inc.", "O=Ollama Inc.")
OLLAMA_MAC_TEAM = "3MU9H2V9Y9"
OLLAMA_START_WAIT_S = 90.0
OLLAMA_STOP_WAIT_S = 15.0

ENDPOINT_HINT = (
    "If endpoint protection software quarantined the file, restore it and see "
    '"Endpoint protection" in README.md.'
)


class ToolError(Exception):
    """A failure whose message is written for the person at the keyboard."""


def run_hint(arguments: str = "", platform: Optional[str] = None) -> str:
    """A dc.py command line as typed on this platform. macOS and Linux have no `python`."""
    interpreter = "python" if (platform or sys.platform) == "win32" else "python3"
    return f"{interpreter} dc.py {arguments}".rstrip()


# Data folder


def resolve_data_dir(
    env: Mapping[str, str],
    platform: str,
    home: str | PurePath,
    configured: Optional[str] = None,
) -> PurePath:
    """Apply the rules from docs/security.md. DC_DATA_DIR wins over config."""
    windows = platform == "win32"
    pure = PureWindowsPath if windows else PurePosixPath
    explicit = env.get("DC_DATA_DIR") or configured
    if explicit:
        path = pure(explicit)
        if not path.is_absolute():
            raise ToolError(f"The data folder must be an absolute path, not {explicit!r}.")
        return path
    if windows:
        local = env.get("LOCALAPPDATA")
        base = pure(local) if local else pure(home) / "AppData" / "Local"
        return base / "DigitalCommons"
    if platform == "darwin":
        return pure(home) / "Library" / "Application Support" / "DigitalCommons"
    state = env.get("XDG_STATE_HOME")
    if state and pure(state).is_absolute():
        return pure(state) / "digital-commons"
    return pure(home) / ".local" / "state" / "digital-commons"


def _is_within(path: PurePath, root: PurePath) -> bool:
    try:
        path.relative_to(root)
    except ValueError:
        return False
    return True


def sync_service(
    path: PurePath, env: Mapping[str, str], platform: str, home: str | PurePath
) -> Optional[str]:
    """Name what would copy this folder off the computer. Matches backend/app/security/paths.py."""
    parts = [part.casefold() for part in path.parts]
    if any(part.startswith("onedrive") for part in parts):
        return "OneDrive"
    for name in ("OneDrive", "OneDriveConsumer", "OneDriveCommercial"):
        value = env.get(name)
        if value and _is_within(path, type(path)(value)):
            return "OneDrive"
    if platform == "darwin":
        for part, following in zip(parts, parts[1:]):
            if part == "library" and following == "mobile documents":
                return "iCloud Drive"
            if part == "library" and following == "cloudstorage":
                return "a cloud storage folder"
        home_parts = [part.casefold() for part in PurePosixPath(home).parts]
        inside_home = parts[: len(home_parts)] == home_parts
        if inside_home and parts[len(home_parts) :][:1] in (["desktop"], ["documents"]):
            return "a Desktop or Documents folder, which iCloud can sync"
    return None


def data_folder_problem(
    path: Path, env: Mapping[str, str], platform: str, home: str | PurePath
) -> Optional[str]:
    """Check the path as written and its real location, as the gateway does."""
    candidates: list[PurePath] = [path]
    with contextlib.suppress(OSError):
        candidates.append(path.resolve())
    for candidate in candidates:
        service = sync_service(candidate, env, platform, home)
        if service:
            return service
    return None


def make_private_dir(path: Path) -> None:
    """Create a folder only this user can read. Windows inherits a user-only ACL."""
    if WINDOWS:
        path.mkdir(parents=True, exist_ok=True)
        return
    previous = os.umask(0o077)
    try:
        path.mkdir(parents=True, exist_ok=True)
    finally:
        os.umask(previous)


# Configuration


def _strip_comment(line: str) -> str:
    quote = ""
    for index, char in enumerate(line):
        if quote:
            quote = "" if char == quote else quote
        elif char in "\"'":
            quote = char
        elif char == "#" and (index == 0 or line[index - 1].isspace()):
            return line[:index]
    return line


def parse_simple_yaml(text: str) -> dict[str, str]:
    """Flatten the scalar settings of a YAML file into dotted names.

    This is not a YAML parser. It reads the plain `key: value` settings this tool
    needs and skips list items, so the runner stays free of dependencies.
    """
    flat: dict[str, str] = {}
    stack: list[tuple[int, str]] = []
    for raw in text.splitlines():
        line = _strip_comment(raw).rstrip()
        if not line.strip() or line.lstrip().startswith("- "):
            continue
        match = re.fullmatch(r"(\s*)([^\s:#][^:]*?):(?:\s+(.*))?", line)
        if not match:
            continue
        indent, key, value = len(match[1]), match[2].strip(), (match[3] or "").strip()
        while stack and stack[-1][0] >= indent:
            stack.pop()
        name = ".".join([part for _, part in stack] + [key])
        if value:
            if len(value) >= 2 and value[0] == value[-1] and value[0] in "\"'":
                value = value[1:-1]
            flat[name] = value
        else:
            stack.append((indent, key))
    return flat


def configured_ollama_models(models: Mapping[str, str]) -> list[str]:
    """Distinct model names of the enabled Ollama tiers, in file order."""
    names: list[str] = []
    for key, value in models.items():
        match = re.fullmatch(r"catalog\.ollama\.([^.]+)\.name", key)
        if not match or models.get(f"tiers.{match[1]}.enabled", "true").lower() == "false":
            continue
        if value not in names:
            names.append(value)
    return names


@dataclass(frozen=True)
class Settings:
    host: str
    port: int
    provider: str
    ollama_url: str
    identity_mode: str
    data_dir: Optional[str]
    models: tuple[str, ...]

    @property
    def gateway_url(self) -> str:
        return f"http://{self.host}:{self.port}"


def _config_file(env: Mapping[str, str], name: str, default: Path) -> Path:
    override = env.get(name)
    if not override:
        return default
    path = Path(override)
    return path if path.is_absolute() else ROOT / path


def _read_yaml(path: Path) -> dict[str, str]:
    return parse_simple_yaml(path.read_text(encoding="utf-8")) if path.is_file() else {}


def load_settings(env: Mapping[str, str]) -> Settings:
    config = _config_file(env, "DC_CONFIG", ROOT / "config" / "development.yaml")
    values = _read_yaml(config)
    values.update(_read_yaml(config.with_name(f"{config.stem}.local.yaml")))
    models = _read_yaml(_config_file(env, "DC_MODELS_CONFIG", ROOT / "config" / "models.yaml"))
    try:
        port = int(values.get("gateway.port", "8000"))
    except ValueError:
        raise ToolError(f"gateway.port is not a number: {values['gateway.port']!r}.") from None
    return Settings(
        host=values.get("gateway.host", "127.0.0.1"),
        port=port,
        provider=env.get("DC_PROVIDER") or values.get("inference.default_provider", "ollama"),
        ollama_url=values.get("providers.ollama.base_url", "http://127.0.0.1:11434").rstrip("/"),
        identity_mode=values.get("identity.mode", "local"),
        data_dir=values.get("storage.data_dir"),
        models=tuple(configured_ollama_models(models)),
    )


def data_folder(settings: Settings) -> Path:
    return Path(resolve_data_dir(os.environ, sys.platform, Path.home(), settings.data_dir))


# HTTP. Proxies and redirects are refused, as everywhere else in this project.


class _NoRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):  # noqa: ANN002, ANN003
        return None


_OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}), _NoRedirects())


def http_get(url: str, timeout: float = 3.0) -> tuple[Optional[int], bytes]:
    """Return (status, body). Status is None when nothing answered."""
    try:
        with _OPENER.open(url, timeout=timeout) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.read()
    except (urllib.error.URLError, OSError, ValueError):
        return None, b""


def is_healthy_reply(status: Optional[int], body: bytes) -> bool:
    if status != 200:
        return False
    try:
        return json.loads(body) == {"status": "ok"}
    except ValueError:
        return False


def gateway_healthy(url: str) -> bool:
    return is_healthy_reply(*http_get(f"{url}/healthz", timeout=2.0))


def port_is_free(host: str, port: int) -> bool:
    """A bind test. Unlike a connect test it also catches reserved ports on Windows."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        option = socket.SO_EXCLUSIVEADDRUSE if WINDOWS else socket.SO_REUSEADDR
        probe.setsockopt(socket.SOL_SOCKET, option, 1)
        try:
            probe.bind((host, port))
        except OSError:
            return False
    return True


# Ollama


def interpret_cloud_status(status: Optional[int], body: bytes) -> tuple[str, str]:
    """Read GET /api/status. Only an exact `true` counts as disabled.

    The first value is `disabled`, `not_disabled`, `unknown` (a reply that cannot be
    read) or `unreachable`. The second is detail for display.
    """
    if status is None:
        return "unreachable", ""
    if status != 200:
        return "unknown", f"HTTP {status}"
    try:
        data = json.loads(body)
    except ValueError:
        return "unknown", "the reply was not JSON"
    cloud = data.get("cloud") if isinstance(data, dict) else None
    if not isinstance(data, dict):
        return "unknown", "the reply was not a JSON object"
    source = str(cloud.get("source", "")) if isinstance(cloud, dict) else ""
    if isinstance(cloud, dict) and cloud.get("disabled") is True:
        return "disabled", source
    return "not_disabled", source


def normalize_model_name(name: str) -> str:
    """Ollama matches names case-insensitively and defaults the tag to latest."""
    name = name.strip().lower()
    return name if ":" in name.rsplit("/", 1)[-1] else f"{name}:latest"


def compare_models(configured: Iterable[str], tags: object) -> tuple[list[str], list[str]]:
    """Return (missing, remote) from an /api/tags reply.

    A row with remote fields is a cloud stub with nothing on this computer, so it
    never counts as installed.
    """
    rows = tags.get("models", []) if isinstance(tags, dict) else []
    installed: dict[str, bool] = {}
    for row in rows if isinstance(rows, list) else []:
        if isinstance(row, dict) and (row.get("name") or row.get("model")):
            remote = bool(row.get("remote_model") or row.get("remote_host"))
            installed[normalize_model_name(row.get("name") or row["model"])] = remote
    missing, stubs = [], []
    for name in configured:
        key = normalize_model_name(name)
        if key not in installed:
            missing.append(name)
        elif installed[key]:
            stubs.append(name)
    return missing, stubs


def read_json(url: str) -> object:
    status, body = http_get(url)
    if status != 200:
        return None
    try:
        return json.loads(body)
    except ValueError:
        return None


# ~/.ollama/server.json


def read_server_json(path: Path) -> Optional[dict]:
    """Return the file's settings, or None when it does not exist.

    Refuses anything that is not a JSON object, because rewriting such a file would
    destroy whatever the person put there.
    """
    try:
        text = path.read_text(encoding="utf-8-sig")
    except FileNotFoundError:
        return None
    except (OSError, UnicodeDecodeError) as error:
        raise ToolError(f"{path} could not be read ({error}). It was left unchanged.") from None
    try:
        settings = json.loads(text)
    except ValueError:
        raise ToolError(f"{path} is not valid JSON. It was left unchanged.") from None
    if not isinstance(settings, dict):
        raise ToolError(f"{path} does not hold a JSON object. It was left unchanged.")
    return settings


def render_server_json(settings: Mapping[str, object]) -> str:
    return json.dumps(settings, indent=2, ensure_ascii=False) + "\n"


def next_backup_path(path: Path) -> Path:
    """server.json.bak, then .bak.1 and so on, so an earlier backup is never overwritten."""
    candidate = path.with_name(path.name + ".bak")
    number = 0
    while candidate.exists():
        number += 1
        candidate = path.with_name(f"{path.name}.bak.{number}")
    return candidate


def merge_server_json(path: Path) -> Optional[Path]:
    """Set the cloud key, keeping every other key.

    Returns the backup path, which is None when no file existed. Raises ToolError
    without changing anything if the file cannot be understood. Does nothing when the
    key is already true.
    """
    current = read_server_json(path)
    if current is not None and current.get(CLOUD_KEY) is True:
        return None
    merged = dict(current or {})
    merged[CLOUD_KEY] = True
    path.parent.mkdir(parents=True, exist_ok=True)
    backup = None
    if current is not None:
        backup = next_backup_path(path)
        shutil.copy2(path, backup)
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(render_server_json(merged), encoding="utf-8", newline="\n")
    if current is not None:
        shutil.copymode(path, temporary)
    os.replace(temporary, path)
    return backup


def ollama_restart_note(platform: str) -> str:
    if platform == "win32":
        how = "Quit Ollama from the system tray, then start it again."
    elif platform == "darwin":
        how = (
            "Quit Ollama from the menu bar, then open it again from Applications. If you "
            "started it with `ollama serve` or `brew services`, restart that instead."
        )
    else:
        how = (
            "Run: sudo systemctl restart ollama\n"
            "A systemd service runs as its own user, so it may not read this file. If\n"
            f"`{run_hint('doctor', platform)}` still reports cloud access on, see docs/deployment.md."
        )
    return f"Ollama must be restarted for this to take effect. {how}"


def ollama_variable_note(platform: str) -> str:
    """How to set the switch where this platform's Ollama will read it."""
    if platform == "darwin":
        return (
            "or run\n  launchctl setenv OLLAMA_NO_CLOUD 1\n"
            "then restart Ollama. The menu bar app does not see variables set in a terminal,\n"
            "and launchctl forgets this one when the Mac restarts."
        )
    return "or set OLLAMA_NO_CLOUD=1 for Ollama, then restart Ollama."


def explain_cloud_change(path: Path) -> str:
    current = read_server_json(path)
    merged = dict(current or {})
    merged[CLOUD_KEY] = True
    before = "(this file does not exist yet)" if current is None else render_server_json(current).rstrip()
    keep = (
        "Other settings in the file are kept, and a copy is saved next to it with .bak added."
        if current is not None
        else "The file will be created."
    )
    return (
        "What this setting does: Ollama can pass a chat to a remote service instead of\n"
        "running it on this computer. Digital Commons refuses to use Ollama while that is\n"
        "possible, so chat stays unavailable until it is turned off. Models installed on\n"
        "this computer keep working. Cloud models stop working in every program that uses\n"
        "this copy of Ollama.\n\n"
        f"What will change in {path}\n\n"
        f"Before:\n{before}\n\n"
        f"After:\n{render_server_json(merged).rstrip()}\n\n"
        f"{keep}"
    )


def apply_cloud_step(
    path: Path,
    assume_yes: bool,
    interactive: bool,
    ask: Callable[[str], str] = input,
) -> bool:
    """Explain, ask and merge. Returns True when the file changed."""
    current = read_server_json(path)
    if current is not None and current.get(CLOUD_KEY) is True:
        print(f"{path} already turns cloud access off, so nothing was changed.")
        print(ollama_restart_note(sys.platform))
        return False
    print(explain_cloud_change(path))
    if not assume_yes:
        if not interactive:
            print(
                "\nNo change was made, because there is nobody here to answer. To turn it off, "
                f"run\n  {run_hint('setup --disable-ollama-cloud')}\n"
                f"{ollama_variable_note(sys.platform)}"
            )
            return False
        try:
            answer = ask("\nTurn off Ollama cloud access now? [y/N] ")
        except EOFError:
            answer = ""
        if answer.strip().lower() not in ("y", "yes"):
            print(
                "No change was made. Chat will stay unavailable until cloud access is off. "
                "Demo mode still works."
            )
            return False
    backup = merge_server_json(path)
    print(f"\nUpdated {path}")
    if backup is not None:
        print(f"Saved the previous version as {backup}")
    print(ollama_restart_note(sys.platform))
    return True


# Go


def find_go(path_env: Optional[str], root: Path, windows: bool) -> Optional[Path]:
    """PATH first, then the copy in .tools. Never downloads anything."""
    found = shutil.which("go", path=path_env)
    if found:
        return Path(found)
    local = root / ".tools" / "go" / "bin" / ("go.exe" if windows else "go")
    return local if local.is_file() else None


def go_install_instruction(platform: str) -> str:
    if platform == "win32":
        return "winget install GoLang.Go"
    if platform == "darwin":
        return "brew install go"
    return "sudo snap install go --classic"


def go_missing_message() -> str:
    return (
        "Go was not found on PATH or in .tools/go/bin. Install it with:\n\n"
        f"  {go_install_instruction(sys.platform)}\n\n"
        "or from https://go.dev/dl/, then open a new terminal window and run the command again."
    )


def go_env() -> dict[str, str]:
    """GOTOOLCHAIN=local stops Go fetching a newer toolchain by itself."""
    env = os.environ.copy()
    env.setdefault("GOTOOLCHAIN", "local")
    env.setdefault("CGO_ENABLED", "0")
    return env


def parse_version(text: str) -> tuple[int, ...]:
    match = re.search(r"(\d+(?:\.\d+)*)", text)
    return tuple(int(part) for part in match[1].split(".")) if match else ()


def go_mod_version(go_mod: Path) -> Optional[str]:
    try:
        match = re.search(r"(?m)^go\s+(\S+)", go_mod.read_text(encoding="utf-8"))
    except OSError:
        return None
    return match[1] if match else None


def go_version(go: Path) -> Optional[str]:
    try:
        output = subprocess.run(
            [str(go), "version"], capture_output=True, text=True, timeout=20, env=go_env()
        ).stdout
    except (OSError, subprocess.SubprocessError):
        return None
    match = re.search(r"go(\d+(?:\.\d+)*)", output)
    return match[1] if match else None


def find_main_package(terminal: Path) -> str:
    """The terminal's main package, relative to terminal/."""
    if (terminal / "main.go").is_file():
        return "."
    candidates = sorted((terminal / "cmd").glob("*/main.go"))
    named = [path for path in candidates if path.parent.name == "digital-commons"]
    chosen = (named or candidates)[:1]
    if not chosen:
        raise ToolError(f"No main package was found under {terminal}.")
    return f"./cmd/{chosen[0].parent.name}"


def terminal_sources(terminal: Path) -> Iterator[Path]:
    yield from terminal.rglob("*.go")
    for name in ("go.mod", "go.sum"):
        if (terminal / name).is_file():
            yield terminal / name


def needs_build(binary: Path, sources: Iterable[Path]) -> bool:
    if not binary.is_file():
        return True
    built = binary.stat().st_mtime
    return any(source.stat().st_mtime > built for source in sources)


def find_go_for_build() -> Path:
    if not (TERMINAL / "go.mod").is_file():
        raise ToolError(f"{TERMINAL / 'go.mod'} was not found, so the terminal cannot be built.")
    go = find_go(os.environ.get("PATH"), ROOT, WINDOWS)
    if go is None:
        raise ToolError(go_missing_message())
    return go


def go_build(go: Path, output: Path, env: Mapping[str, str]) -> None:
    command = [str(go), "build", "-trimpath", "-o", str(output), find_main_package(TERMINAL)]
    try:
        code = subprocess.run(command, cwd=TERMINAL, env=env).returncode
    except OSError as error:
        raise ToolError(f"Go could not be started: {error}\n{ENDPOINT_HINT}") from None
    if code != 0:
        raise ToolError(f"The terminal build failed (exit {code}).\n{ENDPOINT_HINT}")
    if not output.is_file():
        raise ToolError(f"The build finished but {output} is missing.\n{ENDPOINT_HINT}")
    print(f"Built {output}")


def build_terminal() -> None:
    go = find_go_for_build()
    BIN.mkdir(exist_ok=True)
    print(f"Building the terminal with {go}. The first build downloads modules and takes longer.")
    go_build(go, BINARY, go_env())


def build_all_terminals() -> None:
    """Cross-compile. Pure Go with CGO off, so any platform can build for any other."""
    go = find_go_for_build()
    DIST.mkdir(exist_ok=True)
    names = ", ".join(label for label, *_ in CROSS_TARGETS)
    print(f"Building the terminal for {names} with {go}, with CGO off.")
    for label, goos, goarch, name in CROSS_TARGETS:
        print(f"{label}:")
        go_build(go, DIST / name, {**go_env(), "GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0"})
    print(
        "\nThese are the terminal only. The gateway cannot be built this way, because it runs\n"
        "from a Python environment that has to be set up on the Mac itself, with:\n\n"
        f"  {run_hint('setup', 'darwin')}\n\n"
        "That also builds a terminal for that Mac, so copying one from here is rarely needed.\n"
        "A Mac blocks a binary that arrives by download or message until it is notarized."
    )


def ensure_terminal_built() -> None:
    if not TERMINAL.is_dir():
        if BINARY.is_file():
            return
        raise ToolError(f"Neither {BINARY} nor the terminal source folder was found.")
    if needs_build(BINARY, terminal_sources(TERMINAL)):
        build_terminal()


# Python environment


def venv_python(venv: Path = VENV, windows: bool = WINDOWS) -> Path:
    return venv / "Scripts" / "python.exe" if windows else venv / "bin" / "python"


def runtime_python(runtime: Path = RUNTIME, windows: bool = WINDOWS) -> Path:
    """The installer's Python, in the layout python-build-standalone uses."""
    return runtime / "python.exe" if windows else runtime / "bin" / "python3"


def require_venv() -> Path:
    """backend/.venv in a checkout, or the private Python in an installed copy."""
    if RUNTIME.is_dir():
        python = runtime_python(RUNTIME)
        if not python.is_file():
            raise ToolError(f"{python} is missing. Reinstall Digital Commons.")
        return python
    python = venv_python()
    if not python.is_file():
        raise ToolError(f"{VENV} was not found. Run: {run_hint('setup')}")
    return python


def venv_has_pip(python: Path) -> bool:
    if not python.is_file():
        return False
    return subprocess.run([str(python), "-m", "pip", "--version"], capture_output=True).returncode == 0


def create_venv(venv: Path = VENV) -> Path:
    """Use the environment if it works. Rebuild one left without pip by a failed or
    interrupted create, which a missing python3-venv on Ubuntu or Ctrl+C can cause."""
    python = venv_python(venv)
    if venv_has_pip(python):
        print(f"Using the existing virtual environment in {venv}")
        return python
    command = [sys.executable, "-m", "venv", str(venv)]
    if venv.exists():
        print(f"The virtual environment in {venv} is incomplete, so it is being rebuilt")
        command.insert(3, "--clear")
    else:
        print(f"Creating a virtual environment in {venv}")
    code = subprocess.run(command).returncode
    if code != 0 or not venv_has_pip(python):
        raise ToolError(
            f"The virtual environment could not be created (exit {code}). On Ubuntu, "
            "install it with: sudo apt install python3-venv"
        )
    return python


def install_requirements(python: Path) -> None:
    if not REQUIREMENTS.is_file():
        raise ToolError(f"{REQUIREMENTS} was not found.")
    print(f"Installing {REQUIREMENTS.relative_to(ROOT)}. The first time this takes a minute or two.")
    command = [
        str(python), "-m", "pip", "install", "--quiet", "--disable-pip-version-check", "-r", str(REQUIREMENTS)
    ]
    code = subprocess.run(command, cwd=ROOT).returncode
    if code != 0:
        raise ToolError(f"Installing the Python packages failed (exit {code}).")
    print("Python packages are installed.")


# Processes


def _process_start_windows(pid: int) -> Optional[str]:
    import ctypes
    from ctypes import wintypes

    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.OpenProcess.restype = wintypes.HANDLE
    kernel32.OpenProcess.argtypes = (wintypes.DWORD, wintypes.BOOL, wintypes.DWORD)
    kernel32.GetProcessTimes.argtypes = (wintypes.HANDLE, *[ctypes.POINTER(wintypes.FILETIME)] * 4)
    kernel32.GetExitCodeProcess.argtypes = (wintypes.HANDLE, ctypes.POINTER(wintypes.DWORD))
    kernel32.CloseHandle.argtypes = (wintypes.HANDLE,)
    process_query_limited_information, still_active = 0x1000, 259
    handle = kernel32.OpenProcess(process_query_limited_information, False, pid)
    if not handle:
        return None
    try:
        exit_code = wintypes.DWORD()
        times = [wintypes.FILETIME() for _ in range(4)]
        if not kernel32.GetExitCodeProcess(handle, ctypes.byref(exit_code)):
            return None
        if exit_code.value != still_active:
            return None
        if not kernel32.GetProcessTimes(handle, *[ctypes.byref(item) for item in times]):
            return None
        return str((times[0].dwHighDateTime << 32) | times[0].dwLowDateTime)
    finally:
        kernel32.CloseHandle(handle)


def process_start_time(pid: int) -> Optional[str]:
    """A stamp that, with the pid, identifies one process. None if it is not running."""
    if pid <= 1:
        return None
    if WINDOWS:
        return _process_start_windows(pid)
    # A fixed locale, because `dc.py stop` compares this with what `dc.py dev` recorded.
    try:
        output = subprocess.run(
            ["ps", "-o", "lstart=", "-p", str(pid)],
            capture_output=True,
            text=True,
            timeout=5,
            env={**os.environ, "LC_ALL": "C"},
        ).stdout
    except (OSError, subprocess.SubprocessError):
        return None
    return output.strip() or None


_job_handle: object = None


def _bind_children_to_this_process_windows() -> None:
    """Make Windows end everything dc.py starts if dc.py is killed outright.

    dc.py joins the job itself, because the venv's python.exe is only a launcher: the
    real interpreter is a child that could start before the gateway was assigned.
    This is a backstop, so a failure here is ignored and the normal stop path remains.
    """
    global _job_handle
    if _job_handle is not None:
        return
    import ctypes
    from ctypes import wintypes

    class BasicLimit(ctypes.Structure):
        _fields_ = [
            ("PerProcessUserTimeLimit", ctypes.c_int64),
            ("PerJobUserTimeLimit", ctypes.c_int64),
            ("LimitFlags", wintypes.DWORD),
            ("MinimumWorkingSetSize", ctypes.c_size_t),
            ("MaximumWorkingSetSize", ctypes.c_size_t),
            ("ActiveProcessLimit", wintypes.DWORD),
            ("Affinity", ctypes.c_size_t),
            ("PriorityClass", wintypes.DWORD),
            ("SchedulingClass", wintypes.DWORD),
        ]

    class ExtendedLimit(ctypes.Structure):
        _fields_ = [
            ("BasicLimitInformation", BasicLimit),
            ("IoInfo", ctypes.c_uint64 * 6),
            ("ProcessMemoryLimit", ctypes.c_size_t),
            ("JobMemoryLimit", ctypes.c_size_t),
            ("PeakProcessMemoryUsed", ctypes.c_size_t),
            ("PeakJobMemoryUsed", ctypes.c_size_t),
        ]

    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.CreateJobObjectW.restype = wintypes.HANDLE
    kernel32.CreateJobObjectW.argtypes = (wintypes.LPVOID, wintypes.LPCWSTR)
    kernel32.SetInformationJobObject.argtypes = (
        wintypes.HANDLE,
        ctypes.c_int,
        wintypes.LPVOID,
        wintypes.DWORD,
    )
    kernel32.AssignProcessToJobObject.argtypes = (wintypes.HANDLE, wintypes.HANDLE)
    kernel32.GetCurrentProcess.restype = wintypes.HANDLE
    kernel32.CloseHandle.argtypes = (wintypes.HANDLE,)

    job = kernel32.CreateJobObjectW(None, None)
    if not job:
        return
    limits = ExtendedLimit()
    limits.BasicLimitInformation.LimitFlags = 0x2000  # JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
    extended_limit_information = 9
    configured = kernel32.SetInformationJobObject(
        job, extended_limit_information, ctypes.byref(limits), ctypes.sizeof(limits)
    )
    if configured and kernel32.AssignProcessToJobObject(job, kernel32.GetCurrentProcess()):
        _job_handle = job
    else:
        kernel32.CloseHandle(job)


def _attached_to_console() -> bool:
    import ctypes

    return bool(ctypes.windll.kernel32.GetConsoleWindow())


def spawn_gateway(env: Mapping[str, str], log: Optional[Path]) -> subprocess.Popen:
    """Start `python -m app` from backend/. With no log it writes to this console."""
    python = require_venv()
    options: dict = {}
    child_env = {**env, "PYTHONUNBUFFERED": "1"}
    if WINDOWS:
        _bind_children_to_this_process_windows()
        # A new process group lets one Ctrl+Break reach the launcher and the
        # interpreter, and nothing else on this console.
        options["creationflags"] = subprocess.CREATE_NEW_PROCESS_GROUP
        if not _attached_to_console():
            options["creationflags"] |= subprocess.CREATE_NO_WINDOW
    else:
        # macOS has no parent-death signal, so POSIX uses a pipe. The write end stays
        # open in this process alone, and the kernel closes it however dc.py ends. The
        # gateway then reads end of file and shuts down: see backend/app/lifeline.py.
        lifeline, _ = os.pipe()
        options["pass_fds"] = (lifeline,)
        child_env[LIFELINE_VARIABLE] = str(lifeline)
    command = [str(python), "-m", "app"]
    try:
        if log is None:
            return subprocess.Popen(
                command, cwd=BACKEND, env=child_env, stdin=subprocess.DEVNULL, **options
            )
        make_private_dir(log.parent)
        descriptor = os.open(log, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(descriptor, "wb") as output:
            return subprocess.Popen(
                command,
                cwd=BACKEND,
                env=child_env,
                stdin=subprocess.DEVNULL,
                stdout=output,
                stderr=subprocess.STDOUT,
                **options,
            )
    except OSError as error:
        raise ToolError(f"The gateway could not be started: {error}\n{ENDPOINT_HINT}") from None
    finally:
        if "pass_fds" in options:
            os.close(options["pass_fds"][0])


_CTRL_BREAK_HELPER = """
import ctypes, sys
kernel32 = ctypes.windll.kernel32
pid = int(sys.argv[1])
kernel32.FreeConsole()
sent = (
    kernel32.AttachConsole(pid)
    and kernel32.SetConsoleCtrlHandler(None, True)
    and kernel32.GenerateConsoleCtrlEvent(1, pid)
)
sys.exit(0 if sent else 1)
"""


def _ask_to_exit(pid: int) -> None:
    """Request a clean shutdown, which uvicorn honours on SIGTERM and Ctrl+Break."""
    if not WINDOWS:
        with contextlib.suppress(ProcessLookupError, PermissionError):
            os.kill(pid, signal.SIGTERM)
        return
    # A separate process, because attaching to the gateway's console would detach
    # dc.py from its own, and the gateway may be on another console.
    with contextlib.suppress(OSError, subprocess.SubprocessError):
        subprocess.run(
            [sys.executable, "-c", _CTRL_BREAK_HELPER, str(pid)],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            creationflags=subprocess.CREATE_NO_WINDOW,
            timeout=5,
        )


def _force_stop(pid: int) -> None:
    if not WINDOWS:
        with contextlib.suppress(ProcessLookupError, PermissionError):
            os.kill(pid, signal.SIGKILL)
        return
    # /T matters: the pid is the venv launcher, and the gateway is its child.
    taskkill = Path(os.environ.get("SystemRoot", r"C:\Windows")) / "System32" / "taskkill.exe"
    with contextlib.suppress(OSError, subprocess.SubprocessError):
        subprocess.run(
            [str(taskkill), "/PID", str(pid), "/T", "/F"],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            timeout=15,
        )


@dataclass
class GatewayRecord:
    """What the pid file holds. `started` guards against a reused pid."""

    pid: int
    started: Optional[str]
    mode: str
    log: Optional[str] = None


def pid_file(data: Path) -> Path:
    return data / "gateway.pid"


def read_record(data: Path) -> Optional[GatewayRecord]:
    try:
        fields = json.loads(pid_file(data).read_text(encoding="utf-8"))
        record = GatewayRecord(
            pid=int(fields["pid"]),
            started=fields["started"],
            mode=str(fields["mode"]),
            log=fields.get("log"),
        )
    except (OSError, ValueError, KeyError, TypeError):
        return None
    return record


def write_record(data: Path, record: GatewayRecord) -> None:
    make_private_dir(data)
    descriptor = os.open(pid_file(data), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as output:
        json.dump(vars(record), output)


def clear_record(data: Path) -> None:
    with contextlib.suppress(OSError):
        pid_file(data).unlink(missing_ok=True)


def running_record(data: Path) -> Optional[GatewayRecord]:
    """The recorded gateway, only if that exact process is still alive."""
    record = read_record(data)
    if record is None or not record.started:
        return None
    return record if process_start_time(record.pid) == record.started else None


def stop_process(record: GatewayRecord, process: Optional[subprocess.Popen] = None) -> bool:
    """Stop a gateway. Returns False if it had to be forced."""

    def alive() -> bool:
        if process is not None:
            return process.poll() is None
        started = process_start_time(record.pid)
        return started is not None and started == record.started

    def wait(seconds: float) -> bool:
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            if not alive():
                return True
            time.sleep(0.1)
        return not alive()

    if not alive():
        return True
    _ask_to_exit(record.pid)
    if wait(GATEWAY_STOP_S):
        return True
    _force_stop(record.pid)
    wait(5.0)
    return False


_INTERRUPT_SIGNALS = tuple(
    getattr(signal, name) for name in ("SIGINT", "SIGTERM", "SIGHUP", "SIGBREAK") if hasattr(signal, name)
)


def _raise_interrupt(signum: int, frame: object) -> None:
    raise KeyboardInterrupt


def install_signal_handlers() -> None:
    """Turn every way of being told to stop into KeyboardInterrupt.

    Windows ends a process on Ctrl+Break unless a handler is set, and that would
    skip the cleanup that stops the gateway.
    """
    for number in _INTERRUPT_SIGNALS:
        signal.signal(number, _raise_interrupt)


@contextlib.contextmanager
def ignoring_interrupts() -> Iterator[None]:
    """Keep a second Ctrl+C from abandoning a shutdown halfway."""
    previous = {number: signal.signal(number, signal.SIG_IGN) for number in _INTERRUPT_SIGNALS}
    try:
        yield
    finally:
        for number, handler in previous.items():
            signal.signal(number, handler)


def log_tail(path: Optional[str | Path], lines: int = 15) -> str:
    if not path:
        return ""
    try:
        text = Path(path).read_text(encoding="utf-8", errors="replace")
    except OSError:
        return ""
    return "\n".join(text.splitlines()[-lines:])


def wait_for_gateway(
    url: str,
    process: Optional[subprocess.Popen] = None,
    log: Optional[str | Path] = None,
    timeout: float = GATEWAY_WAIT_S,
) -> None:
    """Poll /healthz, reporting progress, until it answers or the wait runs out."""
    print(f"Waiting for the gateway at {url} (up to {timeout:.0f} s)")
    started = time.monotonic()
    next_report = 5.0
    while not gateway_healthy(url):
        if process is not None and process.poll() is not None:
            detail = log_tail(log)
            detail = f"\nThe end of its log, {log}:\n{detail}" if detail else ""
            raise ToolError(f"The gateway stopped during startup (exit {process.returncode}).{detail}")
        elapsed = time.monotonic() - started
        if elapsed >= timeout:
            detail = log_tail(log)
            detail = f"\nThe end of its log, {log}:\n{detail}" if detail else ""
            raise ToolError(
                f"The gateway did not answer within {timeout:.0f} s. "
                f"Check with: {run_hint('doctor')}{detail}"
            )
        if elapsed >= next_report:
            print(f"  still waiting, {elapsed:.0f} s so far")
            next_report += 5.0
        time.sleep(0.25)
    print(f"Gateway ready after {time.monotonic() - started:.1f} s")


def run_terminal(
    env: Mapping[str, str], extra: Iterable[str] = (), config: Path = TERMINAL_CONFIG
) -> int:
    """Run the terminal in the foreground.

    Standard handles are inherited, never piped: on Windows, resize events arrive
    only on the real console input handle.
    """
    if not (sys.stdin.isatty() and sys.stdout.isatty()):
        print("Warning: this is not an interactive console, so the terminal may not start.")
    command = [str(BINARY), "--config", str(config), *extra]
    try:
        process = subprocess.Popen(command, cwd=ROOT, env=env)
    except OSError as error:
        raise ToolError(f"The terminal could not be started: {error}\n{ENDPOINT_HINT}") from None
    try:
        return _wait_polling(process)
    except KeyboardInterrupt:
        # Ctrl+Break and a keyboard Ctrl+C also reach the terminal, which restores the
        # screen before it exits, so give it time before stepping in.
        deadline = time.monotonic() + TERMINAL_GRACE_S
        with ignoring_interrupts():
            while process.poll() is None and time.monotonic() < deadline:
                time.sleep(0.1)
            if process.poll() is None:
                process.terminate()
                process.wait()
        raise


def _wait_polling(process: subprocess.Popen) -> int:
    """Wait so that signals are noticed. Popen.wait() on Windows blocks them."""
    while process.poll() is None:
        time.sleep(0.1)
    return process.returncode


# First start. `start` is what an installed copy runs: it makes sure Ollama is
# installed, running and kept on this computer, and that the configured model is
# downloaded, asking before each change, then opens Digital Commons. A step that is
# already done is skipped, so it is safe to run every time.


def ollama_app_locations(platform: str, home: Path, env: Mapping[str, str]) -> list[Path]:
    """Where Ollama's own installers put it."""
    if platform == "win32":
        local = env.get("LOCALAPPDATA") or str(home / "AppData" / "Local")
        return [Path(local) / "Programs" / "Ollama" / "ollama app.exe"]
    if platform == "darwin":
        return [Path("/Applications/Ollama.app"), home / "Applications" / "Ollama.app"]
    return []


def find_ollama(
    platform: str = sys.platform,
    home: Optional[Path] = None,
    env: Mapping[str, str] = os.environ,
    which: Callable[[str], Optional[str]] = shutil.which,
) -> Optional[Path]:
    for path in ollama_app_locations(platform, home or Path.home(), env):
        if path.exists():
            return path
    found = which("ollama")
    return Path(found) if found else None


def start_ollama(app: Path, platform: str = sys.platform) -> None:
    """Start Ollama as a program of its own, so it outlives this window."""
    if platform == "darwin" and app.suffix == ".app":
        subprocess.run(["open", "-g", str(app)], check=False)
        return
    # The Windows tray app starts the server itself. A bare command needs `serve`.
    command = [str(app)] if app.name.lower() == "ollama app.exe" else [str(app), "serve"]
    options: dict = {}
    if platform == "win32":
        options["creationflags"] = subprocess.DETACHED_PROCESS | subprocess.CREATE_NEW_PROCESS_GROUP
    else:
        options["start_new_session"] = True
    subprocess.Popen(
        command,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        close_fds=True,
        **options,
    )


def stop_ollama(platform: str = sys.platform) -> None:
    """Quit Ollama so that it reads its settings again. Only after the person agreed."""
    if platform == "win32":
        taskkill = Path(os.environ.get("SystemRoot", r"C:\Windows")) / "System32" / "taskkill.exe"
        for image in ("ollama app.exe", "ollama.exe"):
            subprocess.run([str(taskkill), "/IM", image, "/F"], capture_output=True)
    elif platform == "darwin":
        subprocess.run(["osascript", "-e", 'tell application "Ollama" to quit'], capture_output=True)
        subprocess.run(["pkill", "-x", "ollama"], capture_output=True)


def server_fallback(app: Path) -> Optional[Path]:
    """The bare server inside an Ollama app, for when the app cannot start one, as on a
    computer with no one signed in to its desktop."""
    if app.name.lower() == "ollama app.exe":
        candidate = app.with_name("ollama.exe")
    elif app.suffix == ".app":
        candidate = app / "Contents" / "Resources" / "ollama"
    else:
        return None
    return candidate if candidate.is_file() else None


def ollama_answers(url: str) -> bool:
    return http_get(f"{url}/api/version", timeout=2.0)[0] == 200


def wait_for_ollama(url: str, up: bool, limit_s: float) -> bool:
    """Wait until Ollama answers, or until it has stopped answering."""
    deadline = time.monotonic() + limit_s
    while time.monotonic() < deadline:
        if ollama_answers(url) == up:
            return True
        time.sleep(1.0)
    return ollama_answers(url) == up


def download(url: str, target: Path) -> None:
    """HTTPS only, through curl, which uses the system's own certificate store."""
    curl = shutil.which("curl")
    if curl is None and WINDOWS:
        curl = str(Path(os.environ.get("SystemRoot", r"C:\Windows")) / "System32" / "curl.exe")
    if curl is None:
        raise ToolError("curl was not found, so Ollama cannot be downloaded. Install it from https://ollama.com/download")
    command = [
        curl, "--fail", "--location", "--proto", "=https", "--proto-redir", "=https",
        "--progress-bar", "--output", str(target), url,
    ]
    if subprocess.run(command).returncode != 0 or not target.is_file():
        raise ToolError(
            f"The download from {url} did not finish. Check the internet connection and try "
            "again, or install Ollama yourself from https://ollama.com/download"
        )


_SIGNATURE_COMMAND = (
    "$s = Get-AuthenticodeSignature -LiteralPath $env:DC_SIGNED_FILE; "
    "[string]$s.Status; [string]$s.SignerCertificate.Subject"
)


def windows_signer_ok(status: str, subject: str) -> bool:
    """Windows trusts the certificate chain, and the subject names Ollama Inc."""
    parts = [part.strip() for part in subject.split(",")]
    return status.strip() == "Valid" and all(name in parts for name in OLLAMA_WINDOWS_SIGNER)


def verify_windows_download(path: Path) -> None:
    powershell = (
        Path(os.environ.get("SystemRoot", r"C:\Windows"))
        / "System32" / "WindowsPowerShell" / "v1.0" / "powershell.exe"
    )
    result = subprocess.run(
        [str(powershell), "-NoProfile", "-NonInteractive", "-Command", _SIGNATURE_COMMAND],
        capture_output=True,
        text=True,
        env={**os.environ, "DC_SIGNED_FILE": str(path)},
    )
    status, subject = (result.stdout.splitlines() + ["", ""])[:2]
    if not windows_signer_ok(status, subject):
        raise ToolError(
            f"The downloaded Ollama installer is not signed by Ollama Inc. (status "
            f"{status or 'unknown'}: {subject or 'no signer'}), so it was not run. Install "
            "Ollama yourself from https://ollama.com/download"
        )


def mac_authority_ok(details: str) -> bool:
    """Read `codesign -dv --verbose=2`: a Developer ID from Ollama's team, chained to
    Apple's root. The team is checked rather than the name, which anyone can choose."""
    lines = details.splitlines()
    authorities = [line.split("=", 1)[1] for line in lines if line.startswith("Authority=")]
    return (
        len(authorities) >= 2
        and authorities[0].startswith("Developer ID Application: ")
        and authorities[0].endswith(f"({OLLAMA_MAC_TEAM})")
        and authorities[-1] == "Apple Root CA"
        and f"TeamIdentifier={OLLAMA_MAC_TEAM}" in lines
    )


def verify_mac_app(app: Path) -> None:
    check = subprocess.run(["codesign", "--verify", "--deep", "--strict", str(app)], capture_output=True, text=True)
    details = subprocess.run(["codesign", "-dv", "--verbose=2", str(app)], capture_output=True, text=True).stderr
    for line in details.splitlines():
        if line.startswith(("Authority=", "TeamIdentifier=")):
            print(f"  {line}")
    if check.returncode != 0 or not mac_authority_ok(details):
        raise ToolError(
            "The downloaded Ollama is not signed with the Apple Developer ID of Ollama's team, so it "
            "was not installed. Install it yourself from https://ollama.com/download"
        )


def install_ollama(platform: str = sys.platform, home: Optional[Path] = None) -> Path:
    """Download Ollama's own installer, check its publisher, and install it."""
    url, name, size = OLLAMA_DOWNLOADS[platform]
    home = home or Path.home()
    with tempfile.TemporaryDirectory(prefix="dc-ollama-") as folder:
        package = Path(folder) / name
        print(f"\nDownloading Ollama ({size}) from {url}")
        download(url, package)
        if platform == "win32":
            verify_windows_download(package)
            print("The download is signed by Ollama Inc. Installing Ollama.")
            code = subprocess.run(
                [str(package), "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/SP-"]
            ).returncode
            if code != 0:
                raise ToolError(f"The Ollama installer stopped with code {code}.")
        else:
            subprocess.run(["ditto", "-x", "-k", str(package), folder], check=True)
            app = Path(folder) / "Ollama.app"
            verify_mac_app(app)
            print("The download carries the Apple Developer ID of Ollama's team. Installing Ollama.")
            applications = Path("/Applications")
            target = applications if os.access(applications, os.W_OK) else home / "Applications"
            target.mkdir(exist_ok=True)
            subprocess.run(["ditto", str(app), str(target / "Ollama.app")], check=True)
    found = find_ollama(platform, home)
    if found is None:
        raise ToolError("Ollama was installed but cannot be found. Start it yourself, then open Digital Commons again.")
    print(f"Ollama is installed: {found}")
    return found


def pull_progress(event: Mapping[str, object]) -> tuple[str, tuple[str, int]]:
    """A line for one /api/pull event, and a key that changes every tenth of a layer."""
    status = str(event.get("status", ""))
    total, completed = event.get("total"), event.get("completed")
    if isinstance(total, int) and total > 0 and isinstance(completed, int):
        share = min(max(completed / total, 0.0), 1.0)
        bar = "#" * int(share * 30)
        line = f"  [{bar:<30}] {share:4.0%}  {completed / 1e9:.1f} of {total / 1e9:.1f} GB"
        return line, (status, int(share * 10))
    return f"  {status}", (status, -1)


def pull_model(url: str, name: str) -> None:
    """Have Ollama download a model, showing its progress."""
    body = json.dumps({"model": name, "stream": True}).encode()
    request = urllib.request.Request(
        f"{url}/api/pull", data=body, headers={"Content-Type": "application/json"}, method="POST"
    )
    live = sys.stdout.isatty()
    last_key: Optional[tuple[str, int]] = None
    done = False
    try:
        # Verifying a large file can take minutes with nothing sent, hence the timeout.
        with _OPENER.open(request, timeout=900) as response:
            for raw in response:
                try:
                    event = json.loads(raw)
                except ValueError:
                    continue
                if not isinstance(event, dict):
                    continue
                if event.get("error"):
                    raise ToolError(f"Ollama could not download {name}: {event['error']}")
                done = done or event.get("status") == "success"
                line, key = pull_progress(event)
                if live:
                    print("\r" + line.ljust(72), end="", flush=True)
                elif key != last_key:
                    print(line)
                last_key = key
    except (urllib.error.URLError, OSError) as error:
        raise ToolError(f"Ollama stopped answering while downloading {name}: {error}") from None
    if live:
        print()
    if not done:
        raise ToolError(f"The download of {name} did not finish. Open Digital Commons again to continue it.")
    print(f"Downloaded {name}.")


@dataclass
class OllamaOps:
    """What `prepare` does to the computer, gathered so that tests can stand in for it."""

    find: Callable[[], Optional[Path]] = find_ollama
    install: Callable[[], Path] = install_ollama
    start: Callable[[Path], None] = start_ollama
    stop: Callable[[], None] = stop_ollama
    wait: Callable[[str, bool, float], bool] = wait_for_ollama
    pull: Callable[[str, str], None] = pull_model


def start_and_wait(ops: OllamaOps, app: Path, url: str) -> bool:
    ops.start(app)
    if ops.wait(url, True, OLLAMA_START_WAIT_S):
        return True
    fallback = server_fallback(app)
    if fallback is None:
        return False
    print("Ollama's app did not start its service, so the service is being started directly.")
    ops.start(fallback)
    return ops.wait(url, True, OLLAMA_START_WAIT_S)


def confirm(question: str, assume_yes: bool, interactive: bool, ask: Callable[[str], str]) -> bool:
    if assume_yes:
        print(f"{question} Yes, as asked in advance.")
        return True
    if not interactive:
        print(f"{question}\nNothing was changed, because there is nobody here to answer.")
        return False
    try:
        answer = ask(f"{question} [Y/n] ")
    except EOFError:
        return False
    return answer.strip().lower() in ("", "y", "yes")


def prepare(
    settings: Settings,
    assume_yes: bool,
    interactive: bool,
    ask: Callable[[str], str] = input,
    ops: Optional[OllamaOps] = None,
    platform: str = sys.platform,
    server_json: Optional[Path] = None,
) -> list[str]:
    """Bring Ollama to a working state, asking before each change. Returns what is
    still wrong, in plain words."""
    if settings.provider != "ollama":
        return []
    ops = ops or OllamaOps()
    server_json = server_json or Path.home() / ".ollama" / "server.json"
    url = settings.ollama_url
    print("Checking Ollama, which runs the AI model on this computer.")

    if not ollama_answers(url):
        app = ops.find()
        if app is None:
            if platform not in OLLAMA_DOWNLOADS:
                return ["Ollama is not installed. Install it from https://ollama.com/download"]
            question = (
                "\nOllama is not installed. Download it from ollama.com "
                f"({OLLAMA_DOWNLOADS[platform][2]}), check that it is signed by Ollama, and "
                "install it now?"
            )
            if not confirm(question, assume_yes, interactive, ask):
                return [
                    "Ollama is not installed, so chat will not work. Open Digital Commons again "
                    "to install it, or install it from https://ollama.com/download"
                ]
            app = ops.install()
        print("Starting Ollama")
        if not start_and_wait(ops, app, url):
            return ["Ollama did not start. Start it yourself, then open Digital Commons again."]

    problems: list[str] = []
    state, detail = interpret_cloud_status(*http_get(f"{url}/api/status"))
    if state == "unknown":
        problems.append(
            f"Ollama did not report its cloud setting ({detail}), so Digital Commons will not "
            "use it. Update Ollama from https://ollama.com/download"
        )
    elif state == "not_disabled":
        print("\nOllama cloud access is on.\n\n" + explain_cloud_change(server_json))
        if confirm("\nTurn off Ollama cloud access and restart Ollama now?", assume_yes, interactive, ask):
            backup = merge_server_json(server_json)
            print(f"Updated {server_json}")
            if backup is not None:
                print(f"Saved the previous version as {backup}")
            print("Restarting Ollama")
            ops.stop()
            ops.wait(url, False, OLLAMA_STOP_WAIT_S)
            app = ops.find()
            restarted = app is not None and start_and_wait(ops, app, url)
            if not restarted or interpret_cloud_status(*http_get(f"{url}/api/status"))[0] != "disabled":
                problems.append(ollama_restart_note(platform))
            else:
                print("Ollama cloud access is off.")
        else:
            problems.append("Ollama cloud access is on, so chat will not work until it is turned off.")
    elif state == "disabled":
        print("Ollama cloud access is off.")

    tags = read_json(f"{url}/api/tags")
    if tags is None:
        problems.append("Ollama did not list its models. Open Digital Commons again in a moment.")
        return problems
    missing, remote = compare_models(settings.models, tags)
    for name in remote:
        problems.append(f"The model {name} points to a remote service. Remove it with: ollama rm {name}")
    for name in missing:
        question = f"\nThe AI model {name} is not downloaded yet. It is several gigabytes. Download it now?"
        if confirm(question, assume_yes, interactive, ask):
            print(f"Downloading {name}. This can take a long time. Leave this window open.")
            ops.pull(url, name)
        else:
            problems.append(f"The AI model {name} is not downloaded, so chat will not work. Open Digital Commons again to download it.")
    if not missing and not remote:
        print("The AI model is ready: " + ", ".join(settings.models))
    return problems


def user_terminal_config(data: Path) -> Path:
    """An installed copy keeps the terminal's settings, and the choices saved beside them,
    in the person's data folder: they survive an update, and a Mac app cannot write
    inside itself."""
    target = data / "terminal.yaml"
    if not target.is_file():
        make_private_dir(data)
        shutil.copyfile(TERMINAL_CONFIG, target)
    return target


def set_window_title(title: str) -> None:
    if WINDOWS:
        import ctypes

        ctypes.windll.kernel32.SetConsoleTitleW(title)
    elif sys.stdout.isatty():
        sys.stdout.write(f"\033]0;{title}\007")
        sys.stdout.flush()


def pause_before_closing() -> None:
    """A window opened from a shortcut closes when this ends, taking the message with it."""
    if sys.stdin.isatty():
        try:
            input("\nPress Enter to close this window.")
        except EOFError:
            pass


# Commands


def session_env(settings: Settings, data: Path) -> dict[str, str]:
    """Environment shared by a gateway and its terminal, so both find the same token."""
    return {**os.environ, "DC_DATA_DIR": str(data)}


def terminal_env(settings: Settings, env: Mapping[str, str]) -> dict[str, str]:
    """A token left in the environment for another gateway would be refused here."""
    result = dict(env)
    if settings.identity_mode == "local":
        result.pop("DC_GATEWAY_TOKEN", None)
    return result


def existing_gateway(settings: Settings, data: Path, mode: str) -> Optional[GatewayRecord | str]:
    """Decide whether a gateway is already running that this session may use.

    Returns its record, "unmanaged" for one dc.py did not start, or None if there is
    none. Raises ToolError for a gateway that must not be mixed with this session.
    """
    record = running_record(data)
    if record is None:
        clear_record(data)
    healthy = gateway_healthy(settings.gateway_url)
    if record is None and not healthy:
        if not port_is_free(settings.host, settings.port):
            raise ToolError(
                f"Port {settings.port} is in use by another program, so the gateway cannot "
                "start. Close that program or change gateway.port in "
                "config/development.local.yaml."
            )
        return None
    current = record.mode if record else None
    if mode == "demo" and current != "demo":
        raise ToolError(
            f"A gateway is already running at {settings.gateway_url} and it is not a demo "
            "gateway. Demo mode runs its own, so that demonstration conversations stay "
            f"separate from real ones. Stop it first with: {run_hint('stop')}"
        )
    if mode != "demo" and current == "demo":
        raise ToolError(
            f"A demo gateway is running at {settings.gateway_url}. Close the demo, or stop "
            f"it with: {run_hint('stop')}"
        )
    return record or "unmanaged"


def run_session(mode: str, terminal_config: Path = TERMINAL_CONFIG) -> int:
    """dev, demo and start: gateway in the background, terminal in the foreground."""
    settings = load_settings(os.environ)
    data = data_folder(settings)
    run_data = data / "demo" if mode == "demo" else data
    env = session_env(settings, run_data)
    if mode == "demo":
        env["DC_PROVIDER"] = "mock"
        print(f"Demo mode: replies are simulated and kept separate in {run_data}")
    ensure_terminal_built()

    existing = existing_gateway(settings, data, mode)
    process = None
    record = None
    code = 1
    try:
        if existing is None:
            require_venv()
            log = run_data / "gateway.log"
            print("Starting the gateway")
            process = spawn_gateway(env, log)
            record = GatewayRecord(process.pid, process_start_time(process.pid), mode, str(log))
            write_record(data, record)
            wait_for_gateway(settings.gateway_url, process, log)
        else:
            print(f"Using the gateway already running at {settings.gateway_url}. It will be left running.")
            wait_for_gateway(settings.gateway_url)
        code = run_terminal(
            terminal_env(settings, env), ["--gateway", settings.gateway_url], terminal_config
        )
    except KeyboardInterrupt:
        code = 130
    finally:
        if process is not None:
            with ignoring_interrupts():
                print("Stopping the gateway")
                stop_process(record or GatewayRecord(process.pid, None, mode), process)
                clear_record(data)
    return code


def cmd_dev(args: argparse.Namespace) -> int:
    return run_session("dev")


def cmd_start(args: argparse.Namespace) -> int:
    set_window_title("Digital Commons")
    try:
        settings = load_settings(os.environ)
        problems = prepare(settings, assume_yes=args.yes, interactive=sys.stdin.isatty())
        if problems:
            print("\nNot everything is ready:")
            for problem in problems:
                print(f"- {problem}")
        if args.prepare_only:
            return 1 if problems else 0
        if problems:
            print("\nDigital Commons will open, but chat will not work until this is fixed.")
        config = user_terminal_config(data_folder(settings)) if RUNTIME.is_dir() else TERMINAL_CONFIG
        code = run_session("dev", config)
    except ToolError as error:
        print(f"\nError: {error}", file=sys.stderr)
        code = 1
    except KeyboardInterrupt:
        return 130
    if code not in (0, 130):
        pause_before_closing()
    return code


def cmd_demo(args: argparse.Namespace) -> int:
    return run_session("demo")


def cmd_backend(args: argparse.Namespace) -> int:
    settings = load_settings(os.environ)
    data = data_folder(settings)
    if existing_gateway(settings, data, "dev") is not None:
        raise ToolError(f"A gateway is already running at {settings.gateway_url}.")
    require_venv()
    process = spawn_gateway(session_env(settings, data), None)
    record = GatewayRecord(process.pid, process_start_time(process.pid), "backend")
    write_record(data, record)
    print(f"Gateway running at {settings.gateway_url}. Press Ctrl+C to stop it.")
    try:
        return _wait_polling(process)
    except KeyboardInterrupt:
        with ignoring_interrupts():
            print("Stopping the gateway")
            stop_process(record, process)
        return 0
    finally:
        clear_record(data)


def cmd_frontend(args: argparse.Namespace) -> int:
    settings = load_settings(os.environ)
    ensure_terminal_built()
    env = terminal_env(settings, session_env(settings, data_folder(settings)))
    # A terminal pointed at an organization gateway needs its token, so an
    # environment token is left alone unless the gateway is loopback and local.
    if os.environ.get("DC_GATEWAY_TOKEN") and terminal_config_is_remote():
        env["DC_GATEWAY_TOKEN"] = os.environ["DC_GATEWAY_TOKEN"]
    try:
        return run_terminal(env)
    except KeyboardInterrupt:
        return 130


def terminal_config_is_remote() -> bool:
    url = _read_yaml(TERMINAL_CONFIG).get("gateway.base_url", "")
    host = re.sub(r"^\w+://", "", url).split("/")[0].rsplit(":", 1)[0]
    return host.lower() not in ("127.0.0.1", "localhost", "")


def cmd_build(args: argparse.Namespace) -> int:
    if args.all:
        build_all_terminals()
    else:
        build_terminal()
    return 0


def cmd_stop(args: argparse.Namespace) -> int:
    settings = load_settings(os.environ)
    data = data_folder(settings)
    record = running_record(data)
    if record is None:
        clear_record(data)
        if gateway_healthy(settings.gateway_url):
            print(
                f"A gateway is answering at {settings.gateway_url}, but dc.py did not start it, "
                "so it was left alone. Stop it where it was started."
            )
            return 1
        print("No gateway is running.")
        return 0
    print(f"Stopping the gateway (pid {record.pid})")
    with ignoring_interrupts():
        clean = stop_process(record)
    if process_start_time(record.pid) == record.started:
        raise ToolError(f"The gateway (pid {record.pid}) could not be stopped.")
    clear_record(data)
    print("Gateway stopped." if clean else "Gateway stopped forcibly, after it did not exit in time.")
    return 0


def clean_targets(root: Path, data: Path, remove_venv: bool) -> list[Path]:
    """Build output and caches. Never .tools, and never anything holding the data folder."""
    protected = {".tools", ".git", ".venv", "node_modules"}
    cache_names = {"__pycache__", ".pytest_cache", ".ruff_cache", ".mypy_cache"}
    targets = [root / "bin", root / "dist"]
    for folder, subfolders, _ in os.walk(root):
        for name in list(subfolders):
            if name in protected:
                subfolders.remove(name)
            elif name in cache_names:
                subfolders.remove(name)
                targets.append(Path(folder) / name)
    if remove_venv:
        targets.append(root / "backend" / ".venv")
    return [path for path in targets if path.exists() and not data.is_relative_to(path)]


def cmd_clean(args: argparse.Namespace) -> int:
    settings = load_settings(os.environ)
    data = data_folder(settings)
    failed = 0
    for path in clean_targets(ROOT, data, args.all):
        try:
            shutil.rmtree(path)
            print(f"Removed {path}")
        except OSError as error:
            failed += 1
            print(f"Could not remove {path}: {error}")
    record = running_record(data)
    if record is not None:
        print(f"Kept the pid file, because a gateway is running (pid {record.pid}). Run: {run_hint('stop')}")
    elif pid_file(data).exists():
        clear_record(data)
        print(f"Removed {pid_file(data)}")
    return 1 if failed else 0


def cmd_test(args: argparse.Namespace) -> int:
    results: list[tuple[str, str]] = []
    python = venv_python()
    if python.is_file():
        env = {**os.environ, "PYTHONPATH": str(BACKEND)}
        for label, folder in (("backend", "tests/backend"), ("tooling", "tests/tooling")):
            print(f"\n== {label}: pytest {folder}")
            if not (ROOT / folder).is_dir():
                results.append((label, f"NOT RUN: {folder} was not found"))
                continue
            code = subprocess.run([str(python), "-m", "pytest", folder], cwd=ROOT, env=env).returncode
            results.append((label, "passed" if code == 0 else f"FAILED (exit {code})"))
    else:
        results.append(("backend", f"NOT RUN: {VENV} was not found. Run: {run_hint('setup')}"))

    go = find_go(os.environ.get("PATH"), ROOT, WINDOWS)
    print("\n== terminal: go test ./...")
    if go is None:
        results.append(("terminal", "NOT RUN: Go was not found. " + go_install_instruction(sys.platform)))
    elif not (TERMINAL / "go.mod").is_file():
        results.append(("terminal", f"NOT RUN: {TERMINAL / 'go.mod'} was not found"))
    else:
        code = subprocess.run([str(go), "test", "./..."], cwd=TERMINAL, env=go_env()).returncode
        results.append(("terminal", "passed" if code == 0 else f"FAILED (exit {code})"))

    print("\nTest summary")
    for label, outcome in results:
        print(f"  {label:<10}{outcome}")
    return 0 if all(outcome == "passed" for _, outcome in results) else 1


def check_ollama_for_setup(settings: Settings, args: argparse.Namespace) -> None:
    server_json = Path.home() / ".ollama" / "server.json"
    state, detail = interpret_cloud_status(*http_get(f"{settings.ollama_url}/api/status"))
    if state == "unreachable":
        print(
            f"Ollama did not answer at {settings.ollama_url}. Install it from "
            "https://ollama.com/download and make sure it is running. Demo mode works without it."
        )
    else:
        tags = read_json(f"{settings.ollama_url}/api/tags")
        missing, remote = compare_models(settings.models, tags)
        for name in missing:
            print(f"Model {name} is not installed. Download it with: ollama pull {name}")
        for name in remote:
            print(f"Model {name} points to a remote service and is not on this computer.")
        if tags is not None and not missing and not remote and settings.models:
            print("The configured model is installed: " + ", ".join(settings.models))

    if state == "disabled":
        print(f"Ollama cloud access is off ({detail or 'source not reported'}).")
    elif state == "unknown":
        print(
            f"Ollama did not report its cloud setting ({detail}). The gateway refuses to use "
            "Ollama until it does. Update Ollama, then run this command again."
        )
    elif state == "not_disabled" or args.disable_ollama_cloud:
        if state == "not_disabled":
            print("Ollama cloud access is on.\n")
        apply_cloud_step(
            server_json,
            assume_yes=args.disable_ollama_cloud,
            interactive=sys.stdin.isatty(),
        )


def cmd_setup(args: argparse.Namespace) -> int:
    settings = load_settings(os.environ)
    print(f"Using Python {sys.version.split()[0]} at {sys.executable}\n")

    print("[1 of 3] Python environment")
    python = create_venv()
    install_requirements(python)

    print("\n[2 of 3] Terminal")
    problems: list[str] = []
    try:
        build_terminal()
    except ToolError as error:
        problems.append(str(error))
        print(error)

    print("\n[3 of 3] Ollama")
    try:
        check_ollama_for_setup(settings, args)
    except ToolError as error:
        print(error)

    if problems:
        print("\nSetup is not complete. Fix the problem above and run it again.")
        return 1
    print(f"\nSetup finished. Check everything with: {run_hint('doctor')}")
    print(f"Start with: {run_hint('dev')}     Try it without a model: {run_hint('demo')}")
    return 0


@dataclass
class Check:
    status: str
    label: str
    detail: str


def doctor_checks() -> list[Check]:
    settings = load_settings(os.environ)
    data = data_folder(settings)
    uses_ollama = settings.provider == "ollama"
    needed = "FAIL" if uses_ollama else "info"
    checks: list[Check] = []

    version = sys.version.split()[0]
    good = sys.version_info[:2] >= MIN_PYTHON
    checks.append(Check("ok" if good else "FAIL", "Python", f"{version} at {sys.executable}"))

    go = find_go(os.environ.get("PATH"), ROOT, WINDOWS)
    if not TERMINAL.is_dir():
        checks.append(Check("info", "Go", "not needed, because the terminal comes built"))
    elif go is None:
        checks.append(Check("FAIL", "Go", f"not found. Install with: {go_install_instruction(sys.platform)}"))
    else:
        found = go_version(go)
        wanted = go_mod_version(TERMINAL / "go.mod")
        if found is None:
            checks.append(Check("FAIL", "Go", f"{go} did not run. {ENDPOINT_HINT}"))
        elif wanted and parse_version(found) < parse_version(wanted):
            checks.append(Check("FAIL", "Go", f"{found} at {go}, but terminal/go.mod needs {wanted}"))
        else:
            checks.append(Check("ok", "Go", f"{found} at {go}"))

    installed = RUNTIME.is_dir()
    python = runtime_python(RUNTIME) if installed else venv_python()
    where = RUNTIME if installed else VENV
    if not python.is_file():
        fix = "Reinstall Digital Commons." if installed else f"Run: {run_hint('setup')}"
        checks.append(Check("FAIL", "Environment", f"{python} not found. {fix}"))
    else:
        probe = subprocess.run(
            [str(python), "-c", "import fastapi, uvicorn"], capture_output=True, text=True
        )
        if probe.returncode == 0:
            checks.append(Check("ok", "Environment", f"{where}, packages installed"))
        else:
            checks.append(Check("FAIL", "Environment", f"packages missing. Run: {run_hint('setup')}"))

    if BINARY.is_file():
        checks.append(Check("ok", "Terminal", f"{BINARY}"))
    else:
        checks.append(Check("FAIL", "Terminal", f"{BINARY} not built. Run: {run_hint('build')}"))

    checks.append(Check("info", "Provider", settings.provider))

    url = settings.ollama_url
    about = read_json(f"{url}/api/version")
    cloud_state, cloud_detail = interpret_cloud_status(*http_get(f"{url}/api/status"))
    if cloud_state == "unreachable":
        checks.append(Check(needed, "Ollama", f"no answer at {url}. Is it running?"))
    else:
        version_text = about.get("version", "unknown") if isinstance(about, dict) else "unknown"
        checks.append(Check("ok", "Ollama", f"reachable at {url}, version {version_text}"))

        tags = read_json(f"{url}/api/tags")
        missing, remote = compare_models(settings.models, tags)
        if tags is None:
            checks.append(Check(needed, "Model", "the list of installed models could not be read"))
        elif not settings.models:
            checks.append(Check("info", "Model", "no Ollama model is configured"))
        elif remote:
            checks.append(Check(needed, "Model", f"{', '.join(remote)} is a remote stub, not a local model"))
        elif missing:
            pull = "; ".join(f"ollama pull {name}" for name in missing)
            checks.append(Check(needed, "Model", f"not installed. Run: {pull}"))
        else:
            checks.append(Check("ok", "Model", f"{', '.join(settings.models)} installed, matches config"))

        if cloud_state == "disabled":
            checks.append(Check("ok", "Cloud access", f"off (source: {cloud_detail or 'not reported'})"))
        elif cloud_state == "unknown":
            checks.append(Check(needed, "Cloud access", f"not reported ({cloud_detail}). Chat is refused. Update Ollama."))
        else:
            checks.append(Check(needed, "Cloud access", f"ON. Chat is refused. Run: {run_hint('setup')}"))

    healthy = gateway_healthy(settings.gateway_url)
    record = running_record(data)
    owner = f", started by dc.py {record.mode} (pid {record.pid})" if record else ""
    if healthy:
        checks.append(Check("ok", "Gateway", f"reachable at {settings.gateway_url}{owner}"))
    else:
        checks.append(Check("info", "Gateway", f"not running at {settings.gateway_url}{owner}"))

    if healthy:
        checks.append(Check("ok", f"Port {settings.port}", "in use by the gateway"))
    elif port_is_free(settings.host, settings.port):
        checks.append(Check("ok", f"Port {settings.port}", "free"))
    else:
        checks.append(Check("FAIL", f"Port {settings.port}", "in use by another program, or reserved"))

    service = data_folder_problem(data, os.environ, sys.platform, Path.home())
    if service:
        checks.append(Check("FAIL", "Data folder", f"{data} is inside {service}. The gateway refuses this."))
    else:
        checks.append(Check("info", "Data folder", str(data)))
    return checks


def cmd_doctor(args: argparse.Namespace) -> int:
    checks = doctor_checks()
    print("Digital Commons doctor\n")
    for check in checks:
        print(f"  {check.status:<6}{check.label:<14}{check.detail}")
    failures = sum(check.status == "FAIL" for check in checks)
    print(f"\n{failures} problem(s) found." if failures else "\nNo problems found.")
    return 1 if failures else 0


COMMANDS = (
    ("setup", cmd_setup, "create the Python environment, build the terminal, check Ollama"),
    ("start", cmd_start, "install Ollama and the model if needed, then open Digital Commons"),
    ("dev", cmd_dev, "start the gateway in the background and open the terminal"),
    ("demo", cmd_demo, "as dev, with simulated replies, no model and no Ollama"),
    ("backend", cmd_backend, "run the gateway in the foreground"),
    ("frontend", cmd_frontend, "build if needed and run the terminal"),
    ("test", cmd_test, "run the backend, tooling and terminal tests"),
    ("build", cmd_build, "build the terminal to bin/, or for every platform to dist/"),
    ("stop", cmd_stop, "stop a gateway started by dc.py"),
    ("clean", cmd_clean, "remove build output, caches and the pid file"),
    ("doctor", cmd_doctor, "report on Python, Go, Ollama, the model, cloud access and the gateway"),
)


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="dc.py",
        description="Digital Commons task runner.",
        epilog=f"First run: {run_hint('setup')}. Then: {run_hint('dev')}, or {run_hint('demo')}.",
    )
    commands = parser.add_subparsers(dest="command", metavar="command", title="commands")
    for name, handler, summary in COMMANDS:
        command = commands.add_parser(name, help=summary, description=summary[0].upper() + summary[1:] + ".")
        command.set_defaults(handler=handler)
        if name == "setup":
            command.add_argument(
                "--disable-ollama-cloud",
                action="store_true",
                help="answer yes in advance to turning off Ollama cloud access",
            )
        if name == "start":
            command.add_argument(
                "--yes", action="store_true", help="answer yes in advance to every question"
            )
            command.add_argument(
                "--prepare-only",
                action="store_true",
                help="set Ollama and the model up, then stop without opening Digital Commons",
            )
        if name == "build":
            command.add_argument(
                "--all",
                action="store_true",
                help="cross-compile the terminal only, for macOS arm64, macOS amd64 and Windows amd64",
            )
        if name == "clean":
            command.add_argument(
                "--all", action="store_true", help="also remove the virtual environment"
            )
    return parser


def main(argv: Optional[list[str]] = None) -> int:
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, "reconfigure"):
            stream.reconfigure(errors="replace", line_buffering=True)
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.command is None:
        parser.print_help()
        return 0
    install_signal_handlers()
    try:
        return args.handler(args)
    except ToolError as error:
        print(f"\nError: {error}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("\nStopped.", file=sys.stderr)
        return 130


if __name__ == "__main__":
    sys.exit(main())
