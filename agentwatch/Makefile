BINARY := agentwatch
MODULE := github.com/matteobortolazzo/agent-stack/agentwatch
VERSION ?= dev

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

LDFLAGS := -s -w

PLUGIN_JSON := plugin/.claude-plugin/plugin.json
PLUGIN_VERSION := $(shell sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' $(PLUGIN_JSON) | head -n1)

.PHONY: build clean test test-macos install-macos lint plugin-bin $(PLATFORMS)

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

# plugin-bin builds the current source into the plugin's bin/ and stamps the
# version marker so `claude --plugin-dir ./plugin` uses the local build instead
# of triggering a download of the (possibly unreleased) plugin version.
plugin-bin:
	go build -ldflags "$(LDFLAGS)" -o plugin/bin/$(BINARY) .
	printf '%s\n' "$(PLUGIN_VERSION)" > plugin/bin/.agentwatch-version

test:
	go test ./...

# Smoke test for the macOS SwiftBar frontend (macOS-only; skips without osascript).
test-macos:
	./plugin/macos/test.sh

# Wires SwiftBar's Plugin Folder + symlinks the plugin in (macOS-only). See
# plugin/macos/README.md.
install-macos:
	./plugin/macos/install.sh

$(PLATFORMS):
	$(eval GOOS := $(word 1,$(subst /, ,$@)))
	$(eval GOARCH := $(word 2,$(subst /, ,$@)))
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-$(GOOS)-$(GOARCH) .

dist: $(PLATFORMS)

lint:
	golangci-lint run

clean:
	rm -f $(BINARY)
	rm -rf dist/
	rm -rf plugin/bin/
