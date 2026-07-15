// Package launcher is the native Go port of sandbox/cenci-sand's host-side
// logic: asset-dir resolution, the BASE_TAG content hash, repo scoping
// (sandbox/lib/repo-scope.sh), image builds, forced plugin updates, the
// interactive launch/attach path, prune, and the orphan reaper.
//
// Nothing routes here yet — `cenci sandbox <verb>` and `cenci open` still
// shell out to the cenci-sand bash launcher. Tickets 2–3 flip those verbs to
// this package and ticket 4 deletes the bash script; until then the two
// implementations coexist and this one is exercised by its own tests only.
//
// The container/entrypoint contract is preserved byte-for-byte from
// cenci-sand: the `cenci-sand.lifecycle=detached` label, `--user root` at
// create time (entrypoint.sh remaps 'dev' to HOST_UID/HOST_GID then drops
// privileges), the HOST_UID/HOST_GID/WORKSPACE_SCOPE env contract, the PID-1
// `touch /tmp/cenci-ready && exec sleep infinity` readiness command, and
// explicit `-u dev` on every exec after the privilege-drop entrypoint (see
// sandbox/CLAUDE.md: `--user` persists as Config.User for the container's
// whole lifetime).
package launcher

// MonolithImage is the shared image tag used when a repo does not opt into
// its own per-repo image via <repo-root>/.cenci/Dockerfile.
const MonolithImage = "cenci-sandbox:latest"

// baseImageRepo is the repository name of the content-hash-tagged base image.
const baseImageRepo = "cenci-sandbox-base"
