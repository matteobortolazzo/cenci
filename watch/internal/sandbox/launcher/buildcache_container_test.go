package launcher

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt in because this test creates real images. It uses only an Alpine fixture,
// not the large SDK downloads, and removes only its own temporary image tags.
// CENCI_TEST_BUILD_CACHE_RUNTIME=docker go test ./internal/sandbox/launcher -run TestBuildCacheContainer -v
func TestBuildCacheContainer(t *testing.T) {
	runtime := os.Getenv("CENCI_TEST_BUILD_CACHE_RUNTIME")
	if runtime == "" {
		t.Skip("set CENCI_TEST_BUILD_CACHE_RUNTIME=docker or podman for real build-cache coverage")
	}
	if runtime != "docker" && runtime != "podman" {
		t.Fatal("CENCI_TEST_BUILD_CACHE_RUNTIME must be docker or podman")
	}
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(runtime, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", runtime, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("version")
	dir := t.TempDir()
	assets := filepath.Join(dir, "assets")
	repo := filepath.Join(dir, "repo")
	image := fmt.Sprintf("cenci-build-cache-test:%d", time.Now().UnixNano())
	// A unique ENV also isolates these cache layers from previous test runs.
	base := fmt.Sprintf(`FROM alpine:latest AS toolchain
ENV CACHE_TEST=%s FOUNDATION_REVISION=one
RUN addgroup -g 1000 dev && adduser -D -u 1000 -G dev dev
USER dev
WORKDIR /home/dev
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
FROM toolchain AS runtime
COPY --chown=dev:dev entrypoint.sh /usr/local/bin/entrypoint.sh
COPY --chown=dev:dev lib/ /usr/local/bin/lib/
`, image)
	recipe := `# cenci:managed-begin
ARG BASE_VERSION=latest
FROM cenci-sandbox-base:${BASE_VERSION}
USER root
RUN sleep 2 && cat /proc/sys/kernel/random/uuid > /stack-token
COPY allowed.txt /allowed.txt
ENV PROJECT_SETTING=preserved
LABEL project.setting=preserved
USER dev
WORKDIR /home/dev
CMD ["project-command"]
# cenci:managed-end
`
	writeFile(t, filepath.Join(assets, "Dockerfile.base"), base)
	writeFile(t, filepath.Join(assets, "entrypoint.sh"), "#!/bin/sh\necho old-entrypoint\n")
	writeFile(t, filepath.Join(assets, "lib", "helper.sh"), "old-helper\n")
	writeFile(t, filepath.Join(assets, "lib", "removed.sh"), "removed-helper\n")
	dockerfile := filepath.Join(repo, ".cenci", "Dockerfile")
	writeFile(t, dockerfile, recipe)
	writeFile(t, filepath.Join(repo, ".cenci", "allowed.txt"), "project-context\n")
	// The original Dockerfile-specific ignore must win over the broad default.
	writeFile(t, filepath.Join(repo, ".cenci", ".dockerignore"), "*\n")
	writeFile(t, dockerfile+".dockerignore", "private.txt\n")
	var log bytes.Buffer
	e := &Engine{Runtime: runtime, AssetDir: assets, Stdout: &log, Stderr: &log}
	ownedImages := []string{image}
	t.Cleanup(func() {
		for _, ref := range ownedImages {
			if out, err := exec.Command(runtime, "image", "rm", ref).CombinedOutput(); err != nil {
				t.Logf("cleanup %s: %v: %s", ref, err, out)
			}
		}
	})
	build := func() {
		t.Helper()
		var err error
		e.BaseTag, err = BaseTag(assets)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(assets, "Dockerfile.base"))
		if err != nil {
			t.Fatal(err)
		}
		ref := fmt.Sprintf("cenci-sandbox-base:toolchain-%x", sha256.Sum256(raw))[:len("cenci-sandbox-base:toolchain-")+12]
		if !strings.Contains(strings.Join(ownedImages, "\n"), ref) {
			ownedImages = append(ownedImages, ref)
		}
		log.Reset()
		started := time.Now()
		if err := e.BuildRepoImage(repo, image); err != nil {
			t.Fatalf("BuildRepoImage: %v\n%s", err, log.String())
		}
		t.Logf("build completed in %s", time.Since(started).Round(time.Millisecond))
	}
	read := func(script string) string {
		t.Helper()
		return run("run", "--rm", "--network=none", "--entrypoint", "/bin/sh", image, "-ec", script)
	}
	build()
	first := read("cat /stack-token")
	if first == "" {
		t.Fatal("dependency RUN did not produce a token")
	}
	writeFile(t, filepath.Join(assets, "entrypoint.sh"), "#!/bin/sh\necho new-entrypoint\n")
	writeFile(t, filepath.Join(assets, "lib", "helper.sh"), "new-helper\n")
	if err := os.Remove(filepath.Join(assets, "lib", "removed.sh")); err != nil {
		t.Fatal(err)
	}
	build()
	if got := read("cat /stack-token"); got != first {
		t.Fatalf("script-only update reran dependency install: %s -> %s", first, got)
	}
	if got := read("cat /usr/local/bin/entrypoint.sh /usr/local/bin/lib/helper.sh; test ! -e /usr/local/bin/lib/removed.sh; cat /allowed.txt; id -u; stat -c %u:%g /usr/local/bin/lib/helper.sh"); got != "#!/bin/sh\necho new-entrypoint\nnew-helper\nproject-context\n1000\n1000:1000" {
		t.Fatalf("final runtime assets/context/user/ownership mismatch: %q", got)
	}
	format := `{{.Config.User}}|{{.Config.WorkingDir}}|{{json .Config.Entrypoint}}|{{json .Config.Cmd}}|{{index .Config.Labels "project.setting"}}|{{index .Config.Labels "cenci.base-version"}}`
	if got, want := run("image", "inspect", "--format", format, image), "dev|/home/dev|[\"/usr/local/bin/entrypoint.sh\"]|[\"project-command\"]|preserved|"+e.BaseTag; got != want {
		t.Fatalf("final metadata = %s, want %s", got, want)
	}
	if got := read("printf %s \"$PROJECT_SETTING\""); got != "preserved" {
		t.Fatalf("project environment lost: %q", got)
	}
	writeFile(t, filepath.Join(assets, "Dockerfile.base"), strings.Replace(base, "FOUNDATION_REVISION=one", "FOUNDATION_REVISION=two", 1))
	build()
	if got := read("cat /stack-token"); got == first {
		t.Fatal("foundation change failed to invalidate dependency RUN")
	}
	if got, err := os.ReadFile(dockerfile); err != nil || string(got) != recipe {
		t.Fatalf("committed project Dockerfile was modified: %v", err)
	}
}
