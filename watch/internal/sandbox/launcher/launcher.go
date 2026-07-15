// Package launcher is the sandbox launch engine behind `cenci sandbox <verb>`
// and `cenci open`/`cn`: asset-dir resolution, the BASE_TAG content hash,
// repo scoping, image builds, forced plugin updates, the interactive
// launch/attach path, prune, and the orphan reaper. It is the native Go port
// of the retired sandbox/cenci-sand bash launcher (and its
// sandbox/lib/repo-scope.sh helper), which it fully replaces.
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
