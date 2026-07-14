module github.com/matteobortolazzo/agent-stack/agentwatch/v2

go 1.25

// v2.0.0-v2.17.1 were tagged before go.mod declared the required /v2
// major-version suffix, so they cannot be resolved as module
// .../agentwatch/v2. Retracted so `go get` skips them and selects a
// valid release. The git tags and GitHub releases are intentionally kept.
retract [v2.0.0, v2.17.1]
