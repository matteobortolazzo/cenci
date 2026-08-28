module github.com/matteobortolazzo/cenci/watch/v2

go 1.25

// v2.0.0 was tagged before the go.mod module path carried the required
// /v2 suffix, making it unfetchable via `go get`/`go list -m` per Go's
// semantic import versioning rules. See #1118.
retract v2.0.0
