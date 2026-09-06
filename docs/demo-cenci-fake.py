#!/usr/bin/env python3
"""Fake cenci daemon for the README GIF recording.

Two pieces, both scoped to a throwaway $CENCI_SOCKET_DIR so they die with the
recording shell and never touch a real daemon:

1. Broadcast socket — serves scripted StateSnapshot NDJSON lines (the daemon's
   public wire format, see watch/pkg/watch) on
   $CENCI_SOCKET_DIR/cenci.sock, so the demo board shows live agent
   badges, status-bar counts, and the ⟳ dispatch segment without real agents.
   CENCI_SOCKET_DIR is cenci's tier-1 socket-dir override (#1142): it wins
   verbatim, so a pkg/watch consumer (like lazyboards) resolves to exactly
   this directory, no appended "cenci/" segment.

2. cenci CLI shim — written to $CENCI_SOCKET_DIR/bin/cenci (demo.tape
   prepends that dir to PATH). It answers the `cenci version` / `cenci
   dispatch status --json` queries lazyboards' dispatch panel makes, absorbs
   everything else, and records each `cenci run <skill> <number>` as a flag
   file. The broadcast loop picks flags up on its next 1s tick and adds a
   running `<number>-<skill>` window — so pressing a column action in the
   recording makes its agent badge appear live, deterministically, with no
   real agent.

Window names join demo-repo cards by ticket-number prefix: keep them in sync
with the issues seeded by demo-repo-seed.sh (#12/#9 New, #6 Refined) and with
the keys pressed in demo.tape.
"""
import json
import os
import socket
import stat
import threading
import time

socket_dir = os.environ["CENCI_SOCKET_DIR"]

os.makedirs(socket_dir, mode=0o700, exist_ok=True)
sock_path = os.path.join(socket_dir, "cenci.sock")
if os.path.exists(sock_path):
    os.unlink(sock_path)

flags_dir = os.path.join(socket_dir, "cenci-demo-flags")
os.makedirs(flags_dir, mode=0o700, exist_ok=True)

# ---- cenci CLI shim (dispatch panel queries + run-action flags) ----
bin_dir = os.path.join(socket_dir, "bin")
os.makedirs(bin_dir, mode=0o700, exist_ok=True)
shim_path = os.path.join(bin_dir, "cenci")
with open(shim_path, "w") as f:
    f.write(
        f"""#!/bin/sh
# Fake cenci CLI for the README GIF — written by docs/demo-cenci-fake.py.
case "$1" in
  version)
    echo "cenci demo" ;;
  dispatch)
    if [ "$2" = "status" ]; then
      printf '%s\\n' '{{"repo":"you/lazyboards-demo","dir":"~/Repos/lazyboards-demo","enrolled":true,"loop":{{"enabled":true,"daemon_running":true,"interval":"5m","pass_running":false,"last_run_at":"2026-01-01T00:00:00Z","last_dispatched":2,"last_skipped":1}}}}'
    fi ;;
  run)
    # `cenci run <skill> <number> ...` — record it so the fake broadcast
    # adds a running <number>-<skill> window on its next tick.
    touch "{flags_dir}/$3-$2" ;;
  *)
    : ;;
esac
"""
    )
os.chmod(shim_path, stat.S_IRWXU)


def window(name, status, task):
    return {
        "session": "cenci-demo",
        "window_index": "1",
        "window_name": name,
        "task_name": task,
        "status": status,
        "agent": "claude",
        "manually_named": False,
    }


base_windows = [
    window("6-implement", "running", "Implement: replace encoding/json with v2"),
    window("12-refine", "need-input", "Refine: migrate CI to GitHub Actions"),
    window("9-refine", "done", "Refine: add JSON logging mode"),
]

dispatch = {
    "enabled": True,
    "daemon_running": True,
    "interval": "5m",
    "pass_running": False,
    "last_run_at": "2026-01-01T00:00:00Z",
    "last_dispatched": 2,
    "last_skipped": 1,
}


def snapshot_line():
    windows = list(base_windows)
    for flag in sorted(os.listdir(flags_dir)):
        windows.append(window(flag, "running", "Dispatched from the board"))
    counts = {"idle": 0, "running": 0, "done": 0, "stopped": 0, "need_input": 0, "failed": 0}
    for w in windows:
        counts[w["status"].replace("-", "_")] += 1
    snapshot = {
        "timestamp": "2026-01-01T00:00:00Z",
        "windows": windows,
        "summary": {"total": len(windows), **counts},
        "dispatch": dispatch,
    }
    return (json.dumps(snapshot) + "\n").encode()


def serve(conn):
    try:
        while True:
            conn.sendall(snapshot_line())
            time.sleep(1)
    except OSError:
        pass
    finally:
        conn.close()


srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
srv.bind(sock_path)
os.chmod(sock_path, 0o600)
srv.listen(4)
while True:
    conn, _ = srv.accept()
    threading.Thread(target=serve, args=(conn,), daemon=True).start()
