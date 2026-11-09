#!/usr/bin/env python3
"""Build the Digital Commons installer for this computer's platform.

    python installer/build.py               the installer, in dist/
    python installer/build.py --stage-only  only the folder that gets installed, in build/

Windows gets an Inno Setup installer and macOS a disk image holding Digital Commons.app,
one per chip. Each is built on the platform it is for, because the private Python and
its packages are native code. Standard library only. Needs Go, and Inno Setup 6 on
Windows. See "Building the installers" in docs/development.md.
"""

from __future__ import annotations

import argparse
import hashlib
import os
import platform as host
import plistlib
import re
import shutil
import struct
import subprocess
import sys
import tarfile
import urllib.request
import zlib
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
import dc  # noqa: E402

HERE = Path(__file__).resolve().parent
BUILD = ROOT / "build"
DIST = ROOT / "dist"

# A relocatable Python from python-build-standalone, checked against the release's
# SHA256SUMS. Change all three together when moving to a newer release.
PYTHON_RELEASE = "https://github.com/astral-sh/python-build-standalone/releases/download/20261026"
PYTHON_BUILDS = {
    "windows-x64": (
        "cpython-3.12.16+20261026-x86_64-pc-windows-msvc-install_only.tar.gz",
        "590dfcbe3a34c07301e6332e13e2188b98655cd072b08afa45a7998dc4c22bc5",
    ),
    "macos-arm64": (
        "cpython-3.12.16+20261026-aarch64-apple-darwin-install_only.tar.gz",
        "fbf4afef53c2a6b72bb741bc06d5671a9dfaf87bd84e5aa73ac9b3d0d589f14e",
    ),
    "macos-x64": (
        "cpython-3.12.16+20261026-x86_64-apple-darwin-install_only.tar.gz",
        "a962e47df522889eaa7ff0d6a56203f1079f68aabb65ffdbe8455f362bc769f3",
    ),
}

# Parts of the Python distribution the gateway never uses. Fewer files to ship and to scan.
UNUSED_PYTHON = (
    "Lib/idlelib", "Lib/tkinter", "Lib/turtledemo", "Lib/ensurepip", "Lib/test", "tcl", "Scripts",
    "lib/python3.12/idlelib", "lib/python3.12/tkinter", "lib/python3.12/turtledemo",
    "lib/python3.12/ensurepip", "lib/python3.12/test", "lib/tcl8.6", "lib/tk8.6", "lib/itcl4.2.4",
)

# What an installed copy holds besides the private Python and the built terminal.
APP_FILES = ("dc.py", "README.md", "config", "data/sample_documents", "docs", "backend/app", "backend/requirements.txt")

ACCENT = (0x74, 0xB8, 0x92)  # theme Accent, dark
CANVAS = (0x0F, 0x17, 0x20)  # theme Bg, dark


class BuildError(Exception):
    pass


def this_target() -> str:
    machine = host.machine().lower()
    if sys.platform == "win32" and machine in ("amd64", "x86_64"):
        return "windows-x64"
    if sys.platform == "darwin":
        return "macos-arm64" if machine == "arm64" else "macos-x64"
    raise BuildError(f"There is no installer for {sys.platform} on {machine}.")


def app_version() -> str:
    text = (ROOT / "backend" / "app" / "__init__.py").read_text(encoding="utf-8")
    found = re.search(r'__version__\s*=\s*"([^"]+)"', text)
    if not found:
        raise BuildError("backend/app/__init__.py has no __version__.")
    return found[1]


def run(command: list, **options) -> None:
    print("  $ " + " ".join(str(part) for part in command))
    if subprocess.run([str(part) for part in command], **options).returncode != 0:
        raise BuildError(f"{Path(str(command[0])).name} failed.")


# The private Python


def fetch_python(target: str) -> Path:
    name, digest = PYTHON_BUILDS[target]
    cache = BUILD / "cache"
    cache.mkdir(parents=True, exist_ok=True)
    archive = cache / name
    if not archive.is_file() or sha256(archive) != digest:
        print(f"Downloading {name}")
        partial = archive.with_suffix(".part")
        with urllib.request.urlopen(f"{PYTHON_RELEASE}/{name.replace('+', '%2B')}", timeout=60) as reply:
            with partial.open("wb") as out:
                shutil.copyfileobj(reply, out)
        partial.replace(archive)
    if sha256(archive) != digest:
        archive.unlink()
        raise BuildError(f"{name} does not match its published SHA-256, so it was not used.")
    return archive


def sha256(path: Path) -> str:
    hasher = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            hasher.update(block)
    return hasher.hexdigest()


def install_python(archive: Path, runtime: Path) -> Path:
    """Unpack into runtime/, which is where dc.py looks for an installed copy's Python."""
    unpack = runtime.parent / "python-unpack"
    shutil.rmtree(unpack, ignore_errors=True)
    with tarfile.open(archive) as bundle:
        bundle.extractall(unpack, filter="data")
    (unpack / "python").rename(runtime)
    unpack.rmdir()
    python = dc.runtime_python(runtime, windows=sys.platform == "win32")
    run([python, "-m", "pip", "install", "--disable-pip-version-check", "--no-warn-script-location",
         "--only-binary=:all:", "-r", ROOT / "backend" / "requirements.txt"])
    run([python, "-m", "pip", "uninstall", "--yes", "--disable-pip-version-check", "pip"])
    for relative in UNUSED_PYTHON:
        shutil.rmtree(runtime / relative, ignore_errors=True)
    return python


# The folder that gets installed


def stage(target: str, version: str) -> Path:
    root = BUILD / target / "Digital Commons"
    shutil.rmtree(root.parent, ignore_errors=True)
    root.mkdir(parents=True)
    print(f"Staging Digital Commons {version} for {target} in {root}")

    python = install_python(fetch_python(target), root / dc.RUNTIME.name)

    print("Building the terminal")
    binary = root / "bin" / dc.BINARY.name
    binary.parent.mkdir()
    dc.go_build(dc.find_go_for_build(), binary, {**dc.go_env(), "CGO_ENABLED": "0"})

    ignore = shutil.ignore_patterns("__pycache__", "*.pyc", "*.local.yaml", "state.json", ".pytest_cache")
    for relative in APP_FILES:
        source, destination = ROOT / relative, root / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        if source.is_dir():
            shutil.copytree(source, destination, ignore=ignore)
        else:
            shutil.copy2(source, destination)
    (root / "VERSION").write_text(version + "\n", encoding="utf-8")

    # Compiled ahead, and never rechecked, so that nothing writes inside the install.
    run([python, "-m", "compileall", "-q", "--invalidation-mode", "unchecked-hash", root / "backend" / "app"])
    write_png_icon(root / "DigitalCommons.ico", "ico")
    return root


# Packages


def windows_installer(root: Path, version: str) -> Path:
    candidates = [
        os.environ.get("ISCC", ""),
        r"C:\Program Files (x86)\Inno Setup 6\ISCC.exe",
        str(Path(os.environ.get("LOCALAPPDATA", "")) / "Programs" / "Inno Setup 6" / "ISCC.exe"),
    ]
    iscc = next((Path(c) for c in candidates if c and Path(c).is_file()), None)
    if iscc is None:
        raise BuildError("Inno Setup 6 was not found. Install it, or set ISCC to ISCC.exe.")
    DIST.mkdir(exist_ok=True)
    run([iscc, "/Qp", f"/DAppVersion={version}", f"/DStageDir={root}", f"/DOutputDir={DIST}",
         f"/DIconFile={root / 'DigitalCommons.ico'}", HERE / "windows" / "DigitalCommons.iss"])
    return DIST / f"DigitalCommons-{version}-windows-x64-setup.exe"


def mac_disk_image(root: Path, target: str, version: str) -> Path:
    holder = root.parent / "image"
    shutil.rmtree(holder, ignore_errors=True)
    bundle = holder / "Digital Commons.app"
    contents = bundle / "Contents"
    resources = contents / "Resources"
    (contents / "MacOS").mkdir(parents=True)
    resources.mkdir()
    shutil.copytree(root, resources / "app", symlinks=True)
    launcher = contents / "MacOS" / "Digital Commons"
    shutil.copy2(HERE / "macos" / "launcher.sh", launcher)
    shutil.copy2(HERE / "macos" / "start.command", resources / "app" / "start.command")
    for script in (launcher, resources / "app" / "start.command"):
        script.chmod(0o755)
    (resources / "app" / "DigitalCommons.ico").unlink()
    write_png_icon(resources / "AppIcon.icns", "icns")
    with (contents / "Info.plist").open("wb") as plist:
        plistlib.dump({
            "CFBundleName": "Digital Commons",
            "CFBundleDisplayName": "Digital Commons",
            "CFBundleIdentifier": "io.github.bedrotaudio.digital-commons",
            "CFBundleVersion": version,
            "CFBundleShortVersionString": version,
            "CFBundlePackageType": "APPL",
            "CFBundleExecutable": "Digital Commons",
            "CFBundleIconFile": "AppIcon",
            "LSMinimumSystemVersion": "12.0",
            "NSHighResolutionCapable": True,
        }, plist)
    (holder / "Applications").symlink_to("/Applications")
    DIST.mkdir(exist_ok=True)
    image = DIST / f"DigitalCommons-{version}-{target}.dmg"
    image.unlink(missing_ok=True)
    run(["hdiutil", "create", "-volname", "Digital Commons", "-srcfolder", holder, "-ov", "-format", "UDZO", image])
    return image


# The icon: the app's mark, an accent diamond, on its dark canvas. Drawn here so that
# the repository holds no binary image.


def icon_pixels(size: int, samples: int) -> bytes:
    rows = []
    radius, half = size * 0.2, size / 2
    for y in range(size):
        row = bytearray(b"\x00")
        for x in range(size):
            canvas = diamond = 0
            for sy in range(samples):
                for sx in range(samples):
                    px = x + (sx + 0.5) / samples
                    py = y + (sy + 0.5) / samples
                    dx = max(abs(px - half) - (half - radius), 0)
                    dy = max(abs(py - half) - (half - radius), 0)
                    if dx * dx + dy * dy <= radius * radius:
                        canvas += 1
                        if abs(px - half) + abs(py - half) <= size * 0.3:
                            diamond += 1
            total = samples * samples
            share = diamond / canvas if canvas else 0
            color = [round(c + (a - c) * share) for c, a in zip(CANVAS, ACCENT)]
            row += bytes(color) + bytes([round(255 * canvas / total)])
        rows.append(bytes(row))
    return b"".join(rows)


def png(size: int) -> bytes:
    def chunk(kind: bytes, data: bytes) -> bytes:
        return struct.pack(">I", len(data)) + kind + data + struct.pack(">I", zlib.crc32(kind + data) & 0xFFFFFFFF)

    header = struct.pack(">IIBBBBB", size, size, 8, 6, 0, 0, 0)
    pixels = icon_pixels(size, 4 if size <= 256 else 2)
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", header) + chunk(b"IDAT", zlib.compress(pixels, 9)) + chunk(b"IEND", b"")


def write_png_icon(path: Path, kind: str) -> None:
    """A Windows .ico or a macOS .icns, both holding PNG images."""
    if kind == "ico":
        sizes = (16, 32, 48, 256)
        images = [png(size) for size in sizes]
        offset = 6 + 16 * len(images)
        entries = b""
        for size, image in zip(sizes, images):
            entries += struct.pack("<BBBBHHII", size % 256, size % 256, 0, 0, 1, 32, len(image), offset)
            offset += len(image)
        path.write_bytes(struct.pack("<HHH", 0, 1, len(images)) + entries + b"".join(images))
    else:
        body = b"".join(
            code + struct.pack(">I", len(image) + 8) + image
            for code, image in ((b"ic07", png(128)), (b"ic08", png(256)), (b"ic09", png(512)))
        )
        path.write_bytes(b"icns" + struct.pack(">I", len(body) + 8) + body)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--stage-only", action="store_true", help="stop after the folder that gets installed")
    args = parser.parse_args(argv)
    try:
        target, version = this_target(), app_version()
        root = stage(target, version)
        if args.stage_only:
            print(f"\nStaged in {root}")
            return 0
        made = windows_installer(root, version) if target == "windows-x64" else mac_disk_image(root, target, version)
        print(f"\nBuilt {made} ({made.stat().st_size / 1e6:.0f} MB)")
        return 0
    except (BuildError, dc.ToolError) as error:
        print(f"\nError: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
