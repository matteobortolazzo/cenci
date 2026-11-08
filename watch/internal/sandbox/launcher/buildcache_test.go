package launcher

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matteobortolazzo/cenci/watch/v2/internal/exectest"
)

const cacheBaseRecipe = `FROM ubuntu:24.04 AS toolchain
RUN echo packages
USER dev
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
FROM toolchain AS runtime
COPY --chown=dev:dev entrypoint.sh /usr/local/bin/entrypoint.sh
COPY --chown=dev:dev lib/ /usr/local/bin/lib/
`

const cacheRepoRecipe = `# cenci:managed-begin
ARG BASE_VERSION=latest
FROM cenci-sandbox-base:${BASE_VERSION}
RUN echo install-dependencies
USER dev
# cenci:managed-end
`

const cacheImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type cacheBuildFixture struct {
	e         *Engine
	repo      string
	log       string
	overlay   string
	published string
	image     string
	build     func() error
}

// The runtime fake records the real build requests and only publishes a tag
// when the corresponding build succeeds. It does not simulate Docker's cache:
// the container-backed regression verifies actual RUN reuse separately.
func newCacheBuildFixture(t *testing.T, runtime string, repo bool) *cacheBuildFixture {
	t.Helper()
	dir := t.TempDir()
	f := &cacheBuildFixture{
		repo: filepath.Join(dir, "repo with spaces"), log: filepath.Join(dir, "calls"),
		overlay: filepath.Join(dir, "overlay"), published: filepath.Join(dir, "published"),
		image: MonolithImage,
	}
	assets := filepath.Join(dir, "assets")
	writeFile(t, filepath.Join(assets, "Dockerfile.base"), cacheBaseRecipe)
	writeFile(t, filepath.Join(assets, "Dockerfile"), strings.ReplaceAll(strings.ReplaceAll(cacheRepoRecipe, "# cenci:managed-begin\n", ""), "# cenci:managed-end\n", ""))
	writeFile(t, filepath.Join(assets, "entrypoint.sh"), "#!/bin/sh\necho first-entrypoint\n")
	writeFile(t, filepath.Join(assets, "lib", "helper.sh"), "echo first-helper\n")
	writeFile(t, filepath.Join(f.repo, ".cenci", "Dockerfile"), cacheRepoRecipe)
	writeFile(t, filepath.Join(f.repo, ".cenci", "Dockerfile.dockerignore"), "private.txt\n")
	t.Setenv("CACHE_BUILD_LOG", f.log)
	t.Setenv("CACHE_BUILD_OVERLAY", f.overlay)
	t.Setenv("CACHE_BUILD_PUBLISHED", f.published)
	t.Setenv("CACHE_BUILD_FAILURE", "")
	t.Setenv("CACHE_BUILD_IID", cacheImageID)
	t.Setenv("CACHE_BUILD_IMAGES", "")
	body := `#!/bin/sh
printf '%s\t' "$@" >> "$CACHE_BUILD_LOG"
printf '\n' >> "$CACHE_BUILD_LOG"
case "$1" in
images) printf '%s' "$CACHE_BUILD_IMAGES"; exit 0 ;;
image) printf '%s\n' "$CACHE_BUILD_IID"; exit 0 ;;
tag) [ "$CACHE_BUILD_FAILURE" != tag ] || exit 34 ;;
rmi) case "$CACHE_BUILD_FAILURE" in cleanup|overlay-cleanup) exit 35 ;; esac ;;
build)
  shift
  iid=""; target=""; recipe=""; tag=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
    --iidfile) iid="$2"; shift ;;
    --target) target="$2"; shift ;;
    -f) recipe="$2"; shift ;;
    -t) tag="$2"; shift ;;
    esac
    shift
  done
  if [ -n "$target" ]; then
    [ "$CACHE_BUILD_FAILURE" != toolchain ] || exit 31
  elif [ -n "$iid" ]; then
    [ "$CACHE_BUILD_FAILURE" != stack ] || exit 32
    printf '%s\n' "$CACHE_BUILD_IID" > "$iid"
  else
    cat "$recipe" > "$CACHE_BUILD_OVERLAY"
    case "$CACHE_BUILD_FAILURE" in overlay|overlay-cleanup) exit 33 ;; esac
    printf '%s\n' "$tag" > "$CACHE_BUILD_PUBLISHED"
  fi
  ;;
esac
`
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	exectest.WriteExecutable(t, filepath.Join(bin, runtime), body)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out bytes.Buffer
	f.e = &Engine{Runtime: runtime, AssetDir: assets, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &out}
	f.build = f.e.BuildMonolith
	if repo {
		f.image = "cenci-sandbox-fixture:latest"
		f.build = func() error { return f.e.BuildRepoImage(f.repo, f.image) }
	}
	f.refreshTag(t)
	return f
}

func (f *cacheBuildFixture) refreshTag(t *testing.T) {
	t.Helper()
	var err error
	f.e.BaseTag, err = BaseTag(f.e.AssetDir)
	if err != nil {
		t.Fatal(err)
	}
}

func (f *cacheBuildFixture) calls(t *testing.T) [][]string {
	t.Helper()
	raw, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		calls = append(calls, strings.Split(strings.TrimSuffix(line, "\t"), "\t"))
	}
	return calls
}

func cacheArg(args []string, key string) string {
	for i, arg := range args {
		if arg == key && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func cacheStackCall(t *testing.T, calls [][]string) []string {
	t.Helper()
	for _, args := range calls {
		if cacheArg(args, "--iidfile") != "" {
			return args
		}
	}
	t.Fatalf("dependency build missing: %v", calls)
	return nil
}

func TestBuildCache_ScriptChangesPreserveDependencyBase(t *testing.T) {
	for _, runtime := range []string{"docker", "podman"} {
		for _, repo := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/repo=%t", runtime, repo), func(t *testing.T) {
				f := newCacheBuildFixture(t, runtime, repo)
				var firstBase, previousFreshness string
				refs := map[string]bool{}
				for _, change := range []string{"initial", "entrypoint.sh", "lib/helper.sh", "Dockerfile.base"} {
					if change != "initial" {
						path := filepath.Join(f.e.AssetDir, change)
						data, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						writeFile(t, path, string(data)+"# changed\n")
					}
					f.refreshTag(t)
					writeFile(t, f.log, "")
					if err := f.build(); err != nil {
						t.Fatal(err)
					}
					calls := f.calls(t)
					stack := cacheStackCall(t, calls)
					base := cacheArg(stack, "--build-arg")
					if !strings.HasPrefix(base, "BASE_VERSION=toolchain-") {
						t.Fatalf("unstable dependency base: %v", stack)
					}
					if change == "initial" {
						firstBase = base
					}
					if change != "Dockerfile.base" && base != firstBase {
						t.Errorf("%s changed dependency base: %s -> %s", change, firstBase, base)
					}
					if change == "Dockerfile.base" && base == firstBase {
						t.Error("base recipe change reused old dependency base")
					}
					if cacheArg(stack, "-t") != "" || cacheArg(stack, "--label") != "" {
						t.Errorf("dependency build publishes or carries runtime labels: %v", stack)
					}
					context, recipe := f.e.AssetDir, filepath.Join(f.e.AssetDir, "Dockerfile")
					if repo {
						context, recipe = filepath.Join(f.repo, ".cenci"), filepath.Join(f.repo, ".cenci", "Dockerfile")
					}
					if stack[len(stack)-1] != context || cacheArg(stack, "-f") != recipe {
						t.Errorf("original Dockerfile/context changed: %v", stack)
					}
					var overlay []string
					for _, args := range calls {
						if cacheArg(args, "-t") == f.image {
							overlay = args
						}
					}
					if len(overlay) == 0 || overlay[len(overlay)-1] != f.e.AssetDir {
						t.Fatalf("runtime overlay missing/trust context changed: %v", calls)
					}
					if !strings.Contains(strings.Join(overlay, " "), "cenci.base-version="+f.e.BaseTag) {
						t.Errorf("final image lacks current runtime label: %v", overlay)
					}
					if previousFreshness == f.e.BaseTag {
						t.Error("runtime freshness did not change")
					}
					previousFreshness = f.e.BaseTag
					content, err := os.ReadFile(f.overlay)
					if err != nil {
						t.Fatal(err)
					}
					ref := cacheTemporaryRef(t, calls)
					if refs[ref] {
						t.Fatalf("build reused another build's temporary tag %q", ref)
					}
					refs[ref] = true
					assertCacheTagRemoved(t, calls, ref)
					want := "FROM " + ref + "\nCOPY --chown=dev:dev entrypoint.sh /usr/local/bin/entrypoint.sh\nCOPY --chown=dev:dev lib/ /usr/local/bin/lib/\n"
					if string(content) != want {
						t.Errorf("overlay must add only current scripts and inherit metadata; got:\n%s", content)
					}
					for _, path := range []string{cacheArg(stack, "--iidfile"), cacheArg(overlay, "-f")} {
						if _, err := os.Stat(path); !os.IsNotExist(err) {
							t.Errorf("temporary build file not removed: %s (%v)", path, err)
						}
					}
				}
			})
		}
	}
}

func TestBuildCache_FailuresNeverPublishFinalImage(t *testing.T) {
	for _, failure := range []string{"toolchain", "stack", "tag", "overlay", "invalid-id", "empty-id", "multiline-id"} {
		t.Run(failure, func(t *testing.T) {
			f := newCacheBuildFixture(t, "docker", true)
			t.Setenv("CACHE_BUILD_FAILURE", failure)
			wantError := failure
			switch failure {
			case "invalid-id":
				t.Setenv("CACHE_BUILD_IID", "alpine:latest")
				wantError = "image ID"
			case "empty-id":
				t.Setenv("CACHE_BUILD_IID", "")
				wantError = "image ID"
			case "multiline-id":
				t.Setenv("CACHE_BUILD_IID", cacheImageID+"\nRUN false")
				wantError = "image ID"
			}
			if err := f.build(); err == nil || !strings.Contains(err.Error(), wantError) {
				t.Fatalf("error = %v, want %q", err, wantError)
			}
			if _, err := os.Stat(f.published); !os.IsNotExist(err) {
				t.Errorf("failed build published final image: %v", err)
			}
			calls := f.calls(t)
			if failure == "overlay" {
				assertCacheTagRemoved(t, calls, cacheTemporaryRef(t, calls))
			}
			for _, args := range calls {
				if path := cacheArg(args, "--iidfile"); path != "" {
					if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
						t.Errorf("failed build leaked temp dir: %v", err)
					}
				}
				if args[0] == "rmi" && failure != "overlay" {
					t.Errorf("failure removed an image before owning its tag: %v", args)
				}
			}
		})
	}
}

func cacheTemporaryRef(t *testing.T, calls [][]string) string {
	t.Helper()
	for _, args := range calls {
		if args[0] == "tag" {
			if len(args) != 3 || args[1] != cacheImageID || !strings.HasPrefix(args[2], "cenci-sandbox-build:") {
				t.Fatalf("temporary tag does not bind this build's validated image ID: %v", args)
			}
			return args[2]
		}
	}
	t.Fatal("missing unique local dependency-image tag")
	return ""
}

func assertCacheTagRemoved(t *testing.T, calls [][]string, ref string) {
	t.Helper()
	removed := false
	for _, args := range calls {
		if args[0] == "rmi" {
			if len(args) != 3 || args[1] != "--no-prune" || args[2] != ref {
				t.Errorf("cleanup must preserve cached parents and remove only this build's temporary tag: %v", args)
			}
			removed = true
		}
	}
	if !removed {
		t.Error("temporary dependency-image tag leaked")
	}
}

func TestBuildCache_CleanupFailureWarnsWithoutMaskingBuildResult(t *testing.T) {
	for _, failure := range []string{"cleanup", "overlay-cleanup"} {
		t.Run(failure, func(t *testing.T) {
			f := newCacheBuildFixture(t, "docker", true)
			t.Setenv("CACHE_BUILD_FAILURE", failure)
			err := f.build()
			if failure == "cleanup" {
				if err != nil {
					t.Fatalf("cleanup failure hid successful build: %v", err)
				}
				if got, err := os.ReadFile(f.published); err != nil || strings.TrimSpace(string(got)) != f.image {
					t.Fatalf("final image not published: %q (%v)", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "overlay") {
				t.Fatalf("cleanup failure hid overlay build failure: %v", err)
			}
			ref := cacheTemporaryRef(t, f.calls(t))
			if output := f.e.Stderr.(*bytes.Buffer).String(); !strings.Contains(output, "Warning:") || !strings.Contains(output, ref) || !strings.Contains(output, "remove") {
				t.Errorf("cleanup failure not reported with its temporary tag: %s", output)
			}
		})
	}
}

func TestBuildCache_ReusesExistingToolchainBase(t *testing.T) {
	f := newCacheBuildFixture(t, "podman", true)
	sum := sha256.Sum256([]byte(cacheBaseRecipe))
	base := fmt.Sprintf("cenci-sandbox-base:toolchain-%x", sum[:6])
	t.Setenv("CACHE_BUILD_IMAGES", base+"\n")
	// Podman can emit an unprefixed full image ID through --iidfile.
	t.Setenv("CACHE_BUILD_IID", strings.TrimPrefix(cacheImageID, "sha256:"))
	if err := f.build(); err != nil {
		t.Fatal(err)
	}
	for _, args := range f.calls(t) {
		if cacheArg(args, "--target") != "" {
			t.Errorf("existing dependency base rebuilt: %v", args)
		}
	}
	if got, err := os.ReadFile(f.published); err != nil || strings.TrimSpace(string(got)) != f.image {
		t.Fatalf("final image not published: %q (%v)", got, err)
	}
}

func TestBuildCache_LegacyAndCustomRecipesKeepOriginalBuild(t *testing.T) {
	for name, recipe := range map[string]string{
		"unmanaged":         "ARG BASE_VERSION=latest\nFROM cenci-sandbox-base:${BASE_VERSION}\n",
		"custom-after":      cacheRepoRecipe + "RUN echo custom\n",
		"custom-before":     "ARG CUSTOM=1\n" + cacheRepoRecipe,
		"startup-helper":    strings.ReplaceAll(cacheRepoRecipe, "RUN echo install-dependencies", "RUN /usr/local/bin/lib/helper.sh"),
		"entrypoint":        strings.ReplaceAll(cacheRepoRecipe, "RUN echo install-dependencies", "RUN /usr/local/bin/entrypoint.sh"),
		"deferred-trigger":  strings.ReplaceAll(cacheRepoRecipe, "RUN echo install-dependencies", "ONBUILD RUN false"),
		"lowercase-trigger": strings.ReplaceAll(cacheRepoRecipe, "RUN echo install-dependencies", "onbuild RUN false"),
		"multistage":        strings.ReplaceAll(cacheRepoRecipe, "RUN echo install-dependencies", "FROM ubuntu:24.04"),
		"missing-end":       strings.ReplaceAll(cacheRepoRecipe, "# cenci:managed-end\n", ""),
		"duplicate-block":   cacheRepoRecipe + cacheRepoRecipe,
		"escape-directive":  "# escape=`\n" + cacheRepoRecipe,
		"heredoc":           strings.ReplaceAll(cacheRepoRecipe, "RUN echo install-dependencies", "RUN <<EOF\necho install-dependencies\nEOF"),
		"legacy-assets":     cacheRepoRecipe,
	} {
		t.Run(name, func(t *testing.T) {
			f := newCacheBuildFixture(t, "docker", true)
			writeFile(t, filepath.Join(f.repo, ".cenci", "Dockerfile"), recipe)
			if name == "legacy-assets" {
				writeFile(t, filepath.Join(f.e.AssetDir, "Dockerfile.base"), "FROM ubuntu:24.04\n")
			}
			f.refreshTag(t)
			if err := f.build(); err != nil {
				t.Fatal(err)
			}
			for _, args := range f.calls(t) {
				if cacheArg(args, "--iidfile") != "" || cacheArg(args, "--target") != "" {
					t.Errorf("custom/legacy recipe used optimized build: %v", args)
				}
				if cacheArg(args, "-t") == f.image && cacheArg(args, "--build-arg") != "BASE_VERSION="+f.e.BaseTag {
					t.Errorf("legacy build lost complete base: %v", args)
				}
			}
			got, err := os.ReadFile(filepath.Join(f.repo, ".cenci", "Dockerfile"))
			if err != nil || string(got) != recipe {
				t.Fatalf("committed recipe changed: %s (%v)", got, err)
			}
		})
	}
}

func TestPrune_KeepsCurrentToolchainBase(t *testing.T) {
	e, log, _, _ := pruneEngine(t, "")
	writeFile(t, filepath.Join(e.AssetDir, "Dockerfile.base"), cacheBaseRecipe)
	sum := sha256.Sum256([]byte(cacheBaseRecipe))
	current := fmt.Sprintf("cenci-sandbox-base:toolchain-%x", sum[:6])
	stale := "cenci-sandbox-base:toolchain-000000000000"
	t.Setenv("FAKE_IMAGES", current+"\n"+stale+"\n"+e.BaseImage()+"\n")
	if err := e.Prune(false, false); err != nil {
		t.Fatal(err)
	}
	calls := readCallLog(t, log)
	if containsLine(calls, "rmi "+current) {
		t.Error("prune removed the current dependency base")
	}
	if !containsLine(calls, "rmi "+stale) {
		t.Error("prune retained a superseded dependency base")
	}
}
