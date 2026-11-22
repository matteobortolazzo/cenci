"""Reconcile selected host preferences with the sandbox user's native config."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import stat
import sys
import tempfile

import tomlkit


FILES = {"claude": "settings.json", "codex": "config.toml"}
KEYS = {"claude": {"model", "effortLevel", "showTurnDuration"},
        "codex": {"model", "model_reasoning_effort", "model_verbosity"}}
STATE = ".cenci-preferences.json"
LIMIT = 4 * 1024 * 1024


def read(name):
    try:
        fd = os.open(name, os.O_NOFOLLOW | os.O_RDONLY | os.O_NONBLOCK)
    except FileNotFoundError:
        return b""
    with os.fdopen(fd, "rb") as stream:
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            raise ValueError("nonregular file")
        data = stream.read(LIMIT + 1)
        if len(data) > LIMIT:
            raise ValueError("oversized file")
        return data


def atomic_write(name, data, expected=None):
    with tempfile.NamedTemporaryFile(dir=".", delete=False) as tmp:
        try:
            tmp.write(data)
            tmp.flush()
            os.fsync(tmp.fileno())
            if expected is not None and read(name) != expected:
                raise ValueError("concurrent client configuration edit")
            os.replace(tmp.name, name)
            directory = os.open(".", os.O_DIRECTORY | os.O_RDONLY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
        finally:
            Path(tmp.name).unlink(missing_ok=True)


def encode(data):
    return (json.dumps(data, ensure_ascii=False, indent=2) + "\n").encode()


def reconcile(agent, desired, audit):
    filename = FILES[agent]
    raw = read(filename)
    state_raw = read(STATE)
    report = {"imported": [], "overridden": []}
    if not state_raw and not desired:
        return report
    doc = (tomlkit.parse(raw.decode()) if agent == "codex" else
           json.loads(raw) if raw else {})
    if not isinstance(doc, dict):
        raise ValueError("config must be an object")
    # Before replacing the config, persist both possible ownership states.
    # A crash on either side of the rename can be resolved under this lock.
    state = json.loads(state_raw) if state_raw else {}
    previous = state.get("managed", {})
    current_digest = hashlib.sha256(raw).hexdigest()
    if "pending" in state and current_digest != state["before"]:
        pending = state["pending"]
        if current_digest == state["after"]:
            previous = pending
        else:
            # A native client edited the file during recovery. Keep only
            # ownership that is identical on both sides of the transaction.
            previous = {k: previous.get(k) if previous.get(k) == pending.get(k) else None
                        for k in previous.keys() | pending.keys()}
    if not isinstance(previous, dict) or not (previous.keys() | desired.keys()) <= KEYS[agent]:
        raise ValueError("invalid managed preferences")
    managed = {}
    changed = False
    for key in sorted(previous.keys() | desired.keys()):
        local = doc.get(key)
        owned = (key in previous and previous[key] is not None and local == previous[key]
                 and isinstance(local, bool) == isinstance(previous[key], bool))
        if owned and key not in desired:
            del doc[key]
            changed = True
        elif key in desired:
            if not owned and (key in doc or key in previous):
                report["overridden"].append(key)
                # A deletion of an inherited field is an explicit local choice.
                if key in previous:
                    managed[key] = None
                continue
            if doc.get(key) != desired[key]:
                doc[key] = desired[key]
                changed = True
            managed[key] = desired[key]
            report["imported"].append(key)
    if audit:
        return report
    if changed:
        output = tomlkit.dumps(doc).encode() if agent == "codex" else encode(doc)
        atomic_write(STATE, encode({"before": current_digest, "after": hashlib.sha256(output).hexdigest(),
                                    "managed": previous, "pending": managed}))
        atomic_write(filename, output, expected=raw)
    if desired:
        final = encode({"managed": managed, "report": report})
        if final != state_raw or changed:
            atomic_write(STATE, final)
    elif state_raw or changed:
        Path(STATE).unlink(missing_ok=True)
    return report


def main():
    agent, home, mode = sys.argv[1:]
    if mode not in {"apply", "audit"}:
        raise ValueError("invalid mode")
    desired = json.load(sys.stdin)
    if not isinstance(desired, dict) or not desired.keys() <= KEYS[agent]:
        raise ValueError("invalid preference selection")
    directory = Path(home) / ("." + agent)
    if not directory.exists() and not desired:
        return {"imported": [], "overridden": []}
    # The normal entrypoint has already created the client's home. Fail
    # closed on a redirected agent home; no writes may reach a bind mount.
    fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        fcntl.flock(fd, fcntl.LOCK_SH if mode == "audit" else fcntl.LOCK_EX)
        os.fchdir(fd)
        return reconcile(agent, desired, mode == "audit")
    finally:
        os.close(fd)


if __name__ == "__main__":
    try:
        print(json.dumps(main()))
    except (OSError, ValueError, KeyError, TypeError, tomlkit.exceptions.TOMLKitError):
        # Parser exceptions may quote config lines containing secrets.
        sys.exit("Cannot reconcile sandbox preferences. Check the client configuration, ownership record, and file permissions.")
