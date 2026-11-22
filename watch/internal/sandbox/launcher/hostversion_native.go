package launcher

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// npm installs Codex as a JavaScript dispatcher plus a platform package.
// Resolve that package's native client without evaluating the wrapper or any
// imports, NODE_OPTIONS, PATH shims, or scripts in its dependency tree.
func hostNativeClient(agent, entry string) (string, error) {
	path, err := filepath.EvalSymlinks(entry)
	if err != nil {
		return "", err
	}
	if agent == "codex" && strings.HasSuffix(path, "/node_modules/@openai/codex/bin/codex.js") {
		root := filepath.Dir(filepath.Dir(path))
		arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
		triple := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH] + map[string]string{"linux": "-unknown-linux-musl", "darwin": "-apple-darwin"}[runtime.GOOS]
		platformPackage := "codex-" + runtime.GOOS + "-" + arch
		// Match Node's nearest-package selection for global and flat installs.
		pkg := filepath.Join(root, "node_modules", "@openai", platformPackage)
		if _, err := os.Stat(pkg); os.IsNotExist(err) {
			pkg = filepath.Join(filepath.Dir(root), platformPackage)
		} else if err != nil {
			return "", err
		}
		var found []string
		if _, err := os.Stat(pkg); os.IsNotExist(err) {
			pkg = root
		} else if err != nil {
			return "", err
		}
		for _, bin := range []string{"bin", "codex"} {
			candidate := filepath.Join(pkg, "vendor", triple, bin, "codex")
			if _, err := os.Stat(candidate); err == nil {
				found = append(found, candidate)
			} else if !os.IsNotExist(err) {
				return "", err
			}
		}
		if len(found) != 1 {
			return "", fmt.Errorf("cannot identify the installed npm Codex platform binary; configure its native executable path")
		}
		path, err = filepath.EvalSymlinks(found[0])
		if err != nil {
			return "", err
		}
	}
	if err := checkHostVersionPath(path); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return "", fmt.Errorf("configured %s must resolve to a singly linked executable regular file", agent)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	var magic [4]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil {
		return "", err
	}
	// ELF, Mach-O and universal Mach-O headers. The OS loader checks actual
	// platform/architecture compatibility when executing the selected path.
	switch {
	case bytes.Equal(magic[:], []byte{0x7f, 'E', 'L', 'F'}):
	case magic == [4]byte{0xcf, 0xfa, 0xed, 0xfe}, magic == [4]byte{0xca, 0xfe, 0xba, 0xbe}, magic == [4]byte{0xca, 0xfe, 0xba, 0xbf}:
	default:
		return "", fmt.Errorf("unsupported %s wrapper; configure the native executable or the npm Codex entry point", agent)
	}
	return path, nil
}
