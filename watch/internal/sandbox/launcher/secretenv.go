package launcher

import "slices"

// secretEnvSpec names one host secret env var the launcher knows about and
// the agents it is forwarded to. Agents nil means every agent. It carries a
// NAME only, never a value.
type secretEnvSpec struct {
	Name   string
	Agents []string
}

// forwardedEnvVarNames is the built-in spec literal, in forwarding order: the
// order assembleExecEnv emits the bare "-e NAME" tokens in, which the exec-argv
// goldens pin. Production code reads it only through resolveSecretEnvSet.
//
// Forwarding is agent-scoped, via Agents: OpenCode reads ANTHROPIC_API_KEY and
// OPENAI_API_KEY natively, Codex only OPENAI_API_KEY, and Claude neither
// (#490). Classification (secretEnvSet.Has) is agent-independent.
var forwardedEnvVarNames = []secretEnvSpec{
	{Name: "CONTEXT7_API_KEY"},
	{Name: "ANTHROPIC_API_KEY", Agents: []string{"opencode"}},
	{Name: "OPENAI_API_KEY", Agents: []string{"codex", "opencode"}},
}

// secretEnvSet is the resolved set of host secret env var names. It is built
// once per Launch, Audit and `cenci open --dry-run` operation by
// resolveSecretEnvSet and threaded through the call chain: it drives both
// per-exec forwarding (forwardNames, consumed by assembleExecEnv) and
// classification (Has, consumed by isSecretEnvName, classifyEnvNames,
// forwardedEnvVars and dry-run redaction), so the two cannot drift apart.
//
// Invariant: the set holds secret env var NAMES only — never their values. A
// value is read from the host environment at the moment a bare "-e NAME" token
// is emitted (appendSecretEnvPassthrough) and never stored here, in a Posture,
// or in an argv.
//
// The zero value classifies nothing and forwards nothing; every production
// path builds one explicitly.
type secretEnvSet struct {
	specs []secretEnvSpec
	names map[string]bool
}

// newSecretEnvSet builds a set from specs, in order. It copies the specs, so a
// later mutation of the caller's slice (or its Agents slices) cannot change
// the set.
func newSecretEnvSet(specs []secretEnvSpec) secretEnvSet {
	owned := make([]secretEnvSpec, len(specs))
	names := make(map[string]bool, len(specs))
	for i, spec := range specs {
		owned[i] = secretEnvSpec{Name: spec.Name, Agents: slices.Clone(spec.Agents)}
		names[spec.Name] = true
	}
	return secretEnvSet{specs: owned, names: names}
}

// resolveSecretEnvSet resolves the built-in secret-name set. It is the only
// production reader of forwardedEnvVarNames. Resolving cannot fail, so it adds
// no error path to Launch, Audit or DryRun.
func resolveSecretEnvSet() secretEnvSet {
	return newSecretEnvSet(forwardedEnvVarNames)
}

// Has reports whether name is a registered secret env var name, for every
// agent.
func (s secretEnvSet) Has(name string) bool {
	return s.names[name]
}

// forwardNames returns, in registration order, the names forwarded to agent.
func (s secretEnvSet) forwardNames(agent string) []string {
	forwarded := make([]string, 0, len(s.specs))
	for _, spec := range s.specs {
		if spec.Agents == nil || slices.Contains(spec.Agents, agent) {
			forwarded = append(forwarded, spec.Name)
		}
	}
	return forwarded
}
