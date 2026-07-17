# demo.tape — VHS recipe for the cenci README GIF
# Requires: vhs, ttyd, ffmpeg (pacman -S vhs ttyd ffmpeg), python3 (for the
# fake cenci daemon that drives agent badges and the dispatch panel), the
# lazyboards binary, and a seeded demo repo at ~/Repos/lazyboards-demo
# (see docs/demo-repo-seed.sh).
# The recording machine is expected to carry cenci's default global lazyboards
# config (flow/templates/lazyboards-config.yml) — the canonical setup this GIF
# advertises; the hidden .lazyboards.yml below only pins columns and the
# column actions so the recording is deterministic.
# Regenerate with: vhs demo.tape   (from the repo root)

Output docs/assets/cenci-demo.gif

Set Shell "zsh"
Set FontSize 14
Set Width 1400
Set Height 800
Set Theme "Catppuccin Mocha"
Set TypingSpeed 60ms
Set Framerate 30

# ---- Silent setup ----
Hide
# Throwaway runtime dir for the fake daemon (mktemp verified before use —
# an unchecked failure would silently collapse to a root-relative path).
Type@5ms `export XDG_RUNTIME_DIR="$(mktemp -d)" && test -d "$XDG_RUNTIME_DIR" || exit 1` Enter
# Fake cenci daemon: scripted snapshots give deterministic agent badges
# (▶/✓/!) and the ⟳ dispatch segment; it also writes a cenci CLI shim that
# answers the dispatch panel and turns `cenci run` column actions into new
# running badges. Both die with this shell.
Type@5ms "python3 docs/demo-cenci-fake.py &" Enter
Sleep 500ms
Type@5ms `export PATH="$XDG_RUNTIME_DIR/bin:$PATH"` Enter
# The recording shell inherits $TMUX when vhs is launched from inside tmux;
# lazyboards would then scope its Agents modal to that real session and hide
# the fake windows (lazyboards#410). The demo is not "inside" any session.
Type@5ms "unset TMUX" Enter
Type@5ms "cd ~/Repos/lazyboards-demo" Enter
# Pin the demo columns to cenci's default board and its column actions, so
# the recording is deterministic regardless of any personal global config.
Type@5ms "printf 'working_label: Working\ncenci: true\naction_refresh_delay: 0\ncolumns:\n  - name: New\n    actions:\n      R:\n        name: Refine\n        type: shell\n        command: cenci run refine {number}\n  - name: Refined\n    actions:\n      I:\n        name: Implement\n        type: shell\n        command: cenci run implement {number}\n  - name: Planned\n  - name: In Review\n' > .lazyboards.yml" Enter
Type@5ms "clear" Enter
Show

# ---- Launch: badges (! 12-refine, ✓ 9-refine), counts, ⟳ dispatch ----
Type "lazyboards" Enter
Sleep 3s

# ---- Tour the New column ----
Type "j"
Sleep 400ms
Type "j"
Sleep 400ms
Type "k"
Sleep 300ms
Type "k"
Sleep 700ms

# ---- Refined column: ▶ 6-implement badge ----
Tab
Sleep 1500ms

# ---- Dispatch panel: enrollment + loop state ----
Type "d"
Sleep 3500ms
Escape
Sleep 700ms

# ---- Column action: Implement a refined card (badge appears live) ----
Type "j"
Sleep 600ms
Type "I"
Sleep 2500ms

# ---- Wrap through Planned and In Review back to New ----
Tab
Sleep 700ms
Tab
Sleep 700ms
Tab
Sleep 800ms

# ---- Column action: Refine a new card (badge appears live) ----
Type "j"
Sleep 500ms
Type "R"
Sleep 2500ms

# ---- Agents modal: every live cenci-watch window ----
Type "w"
Sleep 3s
Escape
Sleep 500ms

# ---- Quit ----
Type "q"
Sleep 1s
