BINARY := agentwatch
MODULE := github.com/matteobortolazzo/claude-tools/agentwatch
VERSION ?= dev

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

LDFLAGS := -s -w

.PHONY: build clean test lint $(PLATFORMS)

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

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
