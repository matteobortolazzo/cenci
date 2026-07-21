package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/matteobortolazzo/cenci/watch/internal/daemon"
	"github.com/matteobortolazzo/cenci/watch/internal/ipc"
	"github.com/matteobortolazzo/cenci/watch/internal/run"
	"github.com/matteobortolazzo/cenci/watch/internal/sandbox"
	"github.com/matteobortolazzo/cenci/watch/internal/sandbox/launcher"
)

// runSupportBundle implements `cenci support-bundle [--output|-o PATH]
// [--yes|-y]` (ticket #573): collect a sanitized diagnostic archive —
// versions, environment variable NAMES only (never values), daemon
// reachability, config.json (or a placeholder when absent), and per-container
// read-only diagnose + tailed boot log for every known sandbox container —
// into a single .tar.gz. It prints the manifest to stdout before writing
// anything, then confirms (default-deny unless --yes) before creating the
// archive. Collection is entirely in-memory: nothing is written to disk until
// the archive itself, and the archive write refuses to clobber an existing
// file at the target path.
func runSupportBundle(args []string) {
	fs := flag.NewFlagSet("support-bundle", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var output string
	var yes bool
	fs.StringVar(&output, "output", "", "output path for the bundle (default: ./cenci-support-bundle-<UTCstamp>.tar.gz)")
	fs.StringVar(&output, "o", "", "alias for --output")
	fs.BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	fs.BoolVar(&yes, "y", false, "alias for --yes")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "cenci support-bundle: %v\n", err)
		os.Exit(2)
	}
	rejectExtra("cenci support-bundle", fs.Args())

	stamp := time.Now().UTC().Format("20060102T150405Z")
	if output == "" {
		output = "cenci-support-bundle-" + stamp + ".tar.gz"
	}

	entries, manifestText, err := collectSupportBundle()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci support-bundle: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(manifestText)

	if !yes {
		fmt.Fprintf(os.Stderr, "Write bundle to %s? [y/N] ", output)
		reader := bufio.NewReader(os.Stdin)
		confirm := ""
		if line, err := reader.ReadString('\n'); err == nil {
			confirm = strings.TrimRight(line, "\n")
		}
		if confirm != "y" && confirm != "Y" {
			fmt.Println("aborted: support bundle not written")
			return
		}
	}

	topDir := "cenci-support-bundle-" + stamp
	abs, err := writeBundleArchive(output, entries, topDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cenci support-bundle: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("wrote " + abs)
}

// bundleEntry is one in-memory file destined for the archive, keyed by its
// path relative to the archive's top-level "cenci-support-bundle-<UTCstamp>/"
// directory.
type bundleEntry struct {
	Name string
	Data []byte
}

// collectSupportBundle gathers every bundle entry (and the manifest text
// printed alongside it) entirely in memory: nothing touches disk until the
// caller writes the final archive. err is non-nil only for a hard collection
// failure (the sandbox launch engine could not be initialized while
// containers were found to diagnose) — every other read (config.json, image
// versions, per-container logs) degrades to an in-band placeholder instead of
// aborting the whole run.
func collectSupportBundle() (entries []bundleEntry, manifestText string, err error) {
	runtimeName, runtimeErr := sandbox.ContainerRuntime()

	var containers []sandbox.Container
	var listErr error
	if runtimeErr == nil {
		containers, listErr = sandbox.ListContainers(runtimeName)
	}

	entries = append(entries,
		bundleEntry{"versions.txt", []byte(versionsText(runtimeName, runtimeErr, listErr))},
		bundleEntry{"environment.txt", []byte(environmentNamesText())},
		bundleEntry{"daemon-status.txt", []byte(daemonStatusText())},
		bundleEntry{"config.json", configEntryContent()},
	)

	var eng *launcher.Engine
	var buf bytes.Buffer
	if len(containers) > 0 {
		eng, err = launcher.New(os.Stdin, &buf, io.Discard)
		if err != nil {
			return nil, "", fmt.Errorf("initialize sandbox engine: %w", err)
		}
	}

	for _, c := range containers {
		buf.Reset()
		scope := launcher.ScopeForContainer(c.Name, c.Image)
		// Diagnose is a best-effort report: it always returns nil on a
		// successful render (see internal/sandbox/launcher/diagnose.go), so
		// its buffered output is used regardless of the return value.
		_ = eng.Diagnose(scope)
		diagContent := append([]byte(nil), buf.Bytes()...)
		entries = append(entries, bundleEntry{"diagnose-" + c.Name + ".txt", diagContent})

		logData, logErr := exec.Command(runtimeName, "logs", "--tail", "500", c.Name).CombinedOutput()
		if logErr != nil {
			logData = []byte(fmt.Sprintf("(logs unavailable: %v)\n", logErr))
		}
		entries = append(entries, bundleEntry{"logs/boot-" + c.Name + ".log", logData})
	}

	manifestText = buildManifest(entries, len(containers))
	entries = append([]bundleEntry{{"manifest.txt", []byte(manifestText)}}, entries...)

	return entries, manifestText, nil
}

// bundleSecretsCaveat is the shared warning appended to the manifest (both
// the copy printed to stdout before the write-confirmation prompt, and the
// manifest.txt embedded in the archive itself): environment.txt is the
// archive's sole hard sanitization guarantee (variable NAMES only, never
// values). Every other entry — logs/boot-*.log (raw `<runtime> logs`
// output), config.json, and diagnose-*.txt — is collected verbatim and can
// contain secrets (API keys, tokens echoed at container boot, credentials
// stored in config) or host paths.
const bundleSecretsCaveat = "Review before sharing: logs/boot-*.log, config.json, and diagnose-*.txt are collected verbatim and may contain secrets or host paths. environment.txt lists variable NAMES only, never values.\n"

// buildManifest renders the manifest printed to stdout before any write, and
// embedded verbatim as manifest.txt inside the archive: every other entry's
// name and byte size, a "Containers found: N" summary line, and the
// bundleSecretsCaveat.
func buildManifest(entries []bundleEntry, containerCount int) string {
	var b strings.Builder
	b.WriteString("Support bundle manifest:\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "  %s (%d bytes)\n", e.Name, len(e.Data))
	}
	fmt.Fprintf(&b, "Containers found: %d\n", containerCount)
	b.WriteString(bundleSecretsCaveat)
	return b.String()
}

// versionsText reports the cenci binary version and the resolved container
// runtime's name and self-reported version, or notes why the runtime could
// not be resolved. When the runtime resolved but listing its containers
// failed (listErr), that failure is surfaced as a visible "containers:
// unavailable: <err>" line rather than silently presenting as zero
// containers found (#572's failure-visibility-consistency rule).
func versionsText(runtimeName string, runtimeErr error, listErr error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "cenci: %s\n", version)
	if runtimeErr != nil {
		fmt.Fprintf(&b, "runtime: unavailable: %v\n", runtimeErr)
		return b.String()
	}
	fmt.Fprintf(&b, "runtime: %s\n", runtimeName)
	if out, err := exec.Command(runtimeName, "--version").Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		fmt.Fprintf(&b, "runtime version: %s\n", strings.TrimSpace(string(out)))
	} else {
		fmt.Fprintf(&b, "runtime version: unknown\n")
	}
	if listErr != nil {
		fmt.Fprintf(&b, "containers: unavailable: %v\n", listErr)
	}
	return b.String()
}

// environmentNamesText lists every host environment variable NAME (never its
// value), one per line, sorted for a deterministic diff-friendly report. This
// guarantee is scoped to host environment enumeration only, and has no
// opt-out flag: no code path in this function ever writes an environment
// variable's value anywhere in the bundle. It does NOT extend to the rest of
// the archive — logs/boot-*.log (raw `<runtime> logs` output) and
// config.json are collected verbatim elsewhere in this file and can contain
// secrets (API keys, tokens echoed at container boot, credentials stored in
// config). See bundleSecretsCaveat, which is surfaced in the manifest before
// any write.
func environmentNamesText() string {
	envs := os.Environ()
	names := make([]string, 0, len(envs))
	for _, e := range envs {
		name := e
		if idx := strings.IndexByte(e, '='); idx >= 0 {
			name = e[:idx]
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, "\n") + "\n"
}

// daemonStatusText reports whether the cenci daemon is currently reachable
// and its best-effort PID, via the same read-only reachability probe `cenci
// status` uses (never starting a daemon as a side effect).
func daemonStatusText() string {
	info := daemon.Status(ipc.DefaultEventSocketPath(), ipc.DefaultPIDPath())
	return fmt.Sprintf("running: %v\npid: %d\n", info.Running, info.PID)
}

// configEntryContent reads the resolved config.json verbatim, or returns a
// "(not found ...)" placeholder when it cannot be determined or read (e.g.
// no file has ever been written) rather than omitting the entry.
func configEntryContent() []byte {
	path := run.DefaultConfigPath()
	if path == "" {
		return []byte("(not found: cannot determine the default config path)\n")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return []byte(fmt.Sprintf("(not found at %s: %v)\n", path, err))
	}
	return data
}

// writeBundleArchive streams entries into a gzip-compressed tar at
// outputPath, nested under a single top-level topDir/ directory, and returns
// the resolved absolute path written. It never clobbers an existing file
// (O_EXCL) and never writes a partial file: any error during resolution,
// creation, or streaming removes whatever was written before returning. The
// file is requested at mode 0600 (subject to umask) rather than 0644 — per
// bundleSecretsCaveat, the archive can contain config/log secrets, so it
// should not be requested world- or group-readable on a shared host.
func writeBundleArchive(outputPath string, entries []bundleEntry, topDir string) (string, error) {
	abs, err := filepath.Abs(outputPath)
	if err != nil {
		return "", fmt.Errorf("resolve output path %q: %w", outputPath, err)
	}
	parent := filepath.Dir(abs)
	if _, err := os.Stat(parent); err != nil {
		return "", fmt.Errorf("output directory %s: %w", parent, err)
	}

	f, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", abs, err)
	}

	writeErr := writeBundleContents(f, entries, topDir)
	closeErr := f.Close()

	if writeErr != nil {
		_ = os.Remove(abs)
		return "", writeErr
	}
	if closeErr != nil {
		_ = os.Remove(abs)
		return "", closeErr
	}
	return abs, nil
}

// writeBundleContents streams entries as a gzip-compressed tar to w, one
// header+body per entry, each nested under topDir/.
func writeBundleContents(w io.Writer, entries []bundleEntry, topDir string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	now := time.Now()
	for _, e := range entries {
		hdr := &tar.Header{
			Name:    topDir + "/" + e.Name,
			Mode:    0o644,
			Size:    int64(len(e.Data)),
			ModTime: now,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(e.Data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
