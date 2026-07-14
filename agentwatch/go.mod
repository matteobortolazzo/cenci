module github.com/matteobortolazzo/agent-stack/agentwatch/v3

go 1.25

// v3.0.0 was tagged before go.mod declared the required /v3
// major-version suffix, so it cannot be resolved as module
// .../agentwatch/v3. Retracted so `go get` skips it and selects a
// valid release. The git tag and GitHub release are intentionally kept.
retract v3.0.0
