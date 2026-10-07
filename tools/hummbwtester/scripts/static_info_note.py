#!/usr/bin/env python3
"""Advertise a Hummingbird marketplace in the Note of a control service staticInfoConfig.json.

The control service copies the Note string verbatim into the beacons it originates and propagates;
clients read the marketplaces of every on-path AS from the "hummingbird" list of that JSON Note.
Only the Note is modified: every other static info setting, every other Note key, and the entries
of other marketplaces are kept. The helper uses the standard library only, so that it can be piped
to a remote ``sudo python3 -`` without being installed there.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import sys
import tempfile
from typing import Any


class Error(RuntimeError):
    pass


def note_key(config: dict[str, Any]) -> str:
    # The control service parses JSON keys case-insensitively, so honor an existing spelling.
    for key in config:
        if key.lower() == "note":
            return key
    return "Note"


def read_note(config: dict[str, Any]) -> dict[str, Any]:
    raw = config.get(note_key(config), "")
    if raw == "":
        return {}
    if not isinstance(raw, str):
        raise Error("the Note field is not a string; refusing to modify it")
    try:
        note = json.loads(raw)
    except json.JSONDecodeError as err:
        raise Error(f"the Note field is not JSON ({err}); refusing to modify it") from err
    if not isinstance(note, dict):
        raise Error("the Note field is not a JSON object; refusing to modify it")
    entries = note.get("hummingbird", [])
    if not isinstance(entries, list):
        raise Error("the Note hummingbird field is not a list; refusing to modify it")
    return note


def write_note(config: dict[str, Any], note: dict[str, Any]) -> None:
    key = note_key(config)
    if note:
        config[key] = json.dumps(note, indent=2) + "\n"
    else:
        config.pop(key, None)


def ensure_entry(config: dict[str, Any], entry: dict[str, str]) -> bool:
    """Put entry first in the hummingbird list, replacing entries of the same name or address."""
    note = read_note(config)
    current = note.get("hummingbird", [])
    # An entry for the same API address under another name would be a different marketplace to
    # clients, which compare whole entries across the ASes of a path; keep only ours.
    others = [
        item for item in current
        if not (isinstance(item, dict) and (item.get("name") == entry["name"]
                                            or item.get("api_address") == entry["api_address"]))
    ]
    # Clients use the first advertised marketplace that covers the whole path.
    desired = [entry, *others]
    if desired == current:
        return False
    note["hummingbird"] = desired
    write_note(config, note)
    return True


def remove_entry(config: dict[str, Any], name: str) -> bool:
    """Remove the entries with this name, dropping keys that become empty."""
    note = read_note(config)
    current = note.get("hummingbird", [])
    remaining = [item for item in current
                 if not (isinstance(item, dict) and item.get("name") == name)]
    if remaining == current:
        return False
    if remaining:
        note["hummingbird"] = remaining
    else:
        note.pop("hummingbird", None)
    write_note(config, note)
    return True


def load(path: Path) -> dict[str, Any]:
    try:
        config = json.loads(path.read_text())
    except FileNotFoundError:
        return {}
    except json.JSONDecodeError as err:
        raise Error(f"{path} is not valid JSON: {err}") from err
    if not isinstance(config, dict):
        raise Error(f"{path} is not a JSON object")
    return config


def store(path: Path, config: dict[str, Any]) -> None:
    """Atomically replace path, keeping the owner and mode of an existing file."""
    if not config:
        # Only a file that holds nothing but our Note becomes empty; restore its absence.
        path.unlink(missing_ok=True)
        return
    try:
        stat = path.stat()
    except FileNotFoundError:
        stat = None
    descriptor, temporary = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.")
    try:
        with os.fdopen(descriptor, "w") as file:
            json.dump(config, file, indent=2)
            file.write("\n")
        if stat is None:
            os.chmod(temporary, 0o644)
        else:
            os.chmod(temporary, stat.st_mode & 0o7777)
            os.chown(temporary, stat.st_uid, stat.st_gid)
        os.replace(temporary, path)
    except BaseException:
        Path(temporary).unlink(missing_ok=True)
        raise


def entry_argument(value: str) -> dict[str, str]:
    try:
        entry = json.loads(value)
    except json.JSONDecodeError as err:
        raise argparse.ArgumentTypeError(f"invalid entry JSON: {err}") from err
    if (not isinstance(entry, dict) or not all(isinstance(entry.get(key), str) and entry[key]
                                               for key in ("name", "api_address"))):
        raise argparse.ArgumentTypeError("entry must be an object with name and api_address")
    return entry


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="action", required=True)
    ensure = subparsers.add_parser("ensure")
    ensure.add_argument("--file", required=True, type=Path)
    ensure.add_argument("--entry", required=True, type=entry_argument)
    remove = subparsers.add_parser("remove")
    remove.add_argument("--file", required=True, type=Path)
    remove.add_argument("--name", required=True)
    for subparser in (ensure, remove):
        subparser.add_argument("--dry-run", action="store_true",
                               help="report whether the file would change without writing it")
    args = parser.parse_args()
    try:
        config = load(args.file)
        if args.action == "ensure":
            changed = ensure_entry(config, args.entry)
        else:
            # remove
            changed = remove_entry(config, args.name)
        if changed and not args.dry_run:
            store(args.file, config)
    except (Error, OSError) as err:
        print(f"error: {err}", file=sys.stderr)
        return 1
    print(json.dumps({"changed": changed}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
