"""The data folder rules from docs/security.md."""

from __future__ import annotations

import ntpath
import os
import sys
from collections.abc import Mapping
from pathlib import Path, PurePath, PurePosixPath, PureWindowsPath


class DataDirRefused(Exception):
    pass


def resolve_data_dir(
    env: Mapping[str, str],
    platform: str,
    home: str | PurePath,
    configured: str | None = None,
) -> PurePath:
    """DC_DATA_DIR wins over config. Matches resolve_data_dir in dc.py."""
    windows = platform == "win32"
    pure = PureWindowsPath if windows else PurePosixPath
    explicit = env.get("DC_DATA_DIR") or configured
    if explicit:
        path = pure(explicit)
        if not path.is_absolute():
            raise DataDirRefused(f"The data folder must be an absolute path, not {explicit!r}.")
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


ONEDRIVE_VARIABLES = ("OneDrive", "OneDriveConsumer", "OneDriveCommercial")


def _is_within(path: PurePath, root: PurePath) -> bool:
    try:
        path.relative_to(root)
    except ValueError:
        return False
    return True


def sync_service(
    path: PurePath, env: Mapping[str, str], platform: str, home: str | PurePath
) -> str | None:
    """Name what would copy this folder off the computer, or None if nothing would."""
    parts = [part.casefold() for part in path.parts]
    if any(part.startswith("onedrive") for part in parts):
        return "OneDrive"
    for name in ONEDRIVE_VARIABLES:
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


def check_data_dir(path: Path, env: Mapping[str, str]) -> None:
    candidates: list[PurePath] = [path]
    try:
        candidates.append(path.resolve())
    except OSError:
        pass
    for candidate in candidates:
        service = sync_service(candidate, env, sys.platform, Path.home())
        if service:
            raise DataDirRefused(
                f"The data folder {path} is inside {service}, which would copy conversations "
                "off this computer. Set DC_DATA_DIR to a folder outside it."
            )


def folder_keeps_user_only_access(
    path: str | PurePath, env: Mapping[str, str], platform: str, home: str | PurePath
) -> bool:
    """Whether only this user's account can open the data folder.

    On POSIX prepare_data_dir sets 0700 itself. Windows has no such step: a folder
    inherits its ACL, and only the user profile and %LOCALAPPDATA% hand down a user-only
    one. A folder elsewhere, such as directly under C:\\, usually lets every account in.
    Nothing reads the ACL, so this answers from the location alone.
    """
    if platform != "win32":
        return True
    target = PureWindowsPath(ntpath.normpath(str(path)))
    roots = [PureWindowsPath(ntpath.normpath(str(home)))]
    local = env.get("LOCALAPPDATA")
    if local:
        roots.append(PureWindowsPath(ntpath.normpath(local)))
    return any(_is_within(target, root) for root in roots)


def prepare_data_dir(path: Path) -> None:
    """Create the folder. POSIX gets umask 077 and 0700. Windows inherits its ACL.

    See folder_keeps_user_only_access for when that inherited ACL is user-only.
    """
    if sys.platform != "win32":
        os.umask(0o077)
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
        path.chmod(0o700)
    else:
        path.mkdir(parents=True, exist_ok=True)
