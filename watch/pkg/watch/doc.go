// Package watch is the public, stdlib-only client for subscribing to an
// agentwatch daemon's live status stream.
//
// The daemon broadcasts newline-delimited JSON (NDJSON) StateSnapshot messages
// over a Unix domain socket. Consumers discover the socket with
// DefaultSocketPath, connect with Dial, and read snapshots in a loop with
// (*Client).ReadSnapshot. Each snapshot lists every tracked window and its
// current status; the WindowName field (a "<number>-<slug>" string) is the
// stable key external tools use to join agentwatch state onto their own data.
//
// # Compatibility contract
//
// The JSON schema of the types in this package evolves additively only. New
// fields may be added over time, but existing field names, their JSON tags, and
// their semantics are never renamed, removed, or repurposed. Consumers MUST
// ignore unknown fields — encoding/json does this by default — so that decoding
// keeps working when the daemon is upgraded ahead of the client.
//
// This package is a leaf: it imports only the standard library and nothing from
// agentwatch's internal packages, so it is safe to import from external tools.
// Versioning follows the existing agentwatch/v* Go submodule tags.
package watch
