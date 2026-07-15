module github.com/matteobortolazzo/agent-stack/agentwatch/v4

go 1.25

// v4.0.0 was tagged before go.mod declared the required /v4
// major-version suffix, so it cannot be resolved as module
// .../agentwatch/v4. Retracted so `go get` skips it and selects a
// valid release. The git tag and GitHub release are intentionally kept.
retract v4.0.0
