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
// goldens pin. Production code reads it only through builtinSecretEnvSet and
// resolveSecretEnvSet; the user's sandboxEnv.forward names (#1016) are merged
// in after it, never written into it.
//
// Forwarding is agent-scoped, via Agents: OpenCode reads ANTHROPIC_API_KEY and
// OPENAI_API_KEY natively, Codex only OPENAI_API_KEY, and Claude neither
// (#490). Classification (secretEnvSet.Has) is agent-independent.
var forwardedEnvVarNames = []secretEnvSpec{
	{Name: "CONTEXT7_API_KEY"},
	{Name: "ANTHROPIC_API_KEY", Agents: []string{"opencode"}},
	{Name: "OPENAI_API_KEY", Agents: []string{"codex", "opencode"}},
}

// secretEnvSet is the resolved set of host secret env var names: the built-in
// names plus any the user lists in sandboxEnv.forward. It is built once per
// Launch, Audit and `cenci open --dry-run` operation by loadSecretEnvSet and
// threaded through the call chain: it drives both per-exec forwarding
// (forwardNames, consumed by assembleExecEnv) and classification (Has,
// consumed by isSecretEnvName, classifyEnvNames, forwardedEnvVars and dry-run
// redaction), so the two cannot drift apart.
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
	// configured holds the names that came from sandboxEnv.forward, in config
	// order (including any that duplicate a built-in name). The audit's
	// boundary-weakening check reads it; forwarding and classification do not.
	configured []string
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

// builtinSecretEnvSet returns the built-in secret-name set alone, with none of
// the user's configured names merged in. Weakening checks use it as the
// baseline "what the default already forwards"; production launch, dry-run and
// audit paths use loadSecretEnvSet instead.
func builtinSecretEnvSet() secretEnvSet {
	return newSecretEnvSet(forwardedEnvVarNames)
}

// resolveSecretEnvSet merges the user's configured names into the built-in set
// and is pure: it reads no file (loadSecretEnvSet does). The built-in specs
// come first, in registration order. A configured name that duplicates a
// built-in keeps that spec's position and its Agents widen to nil (every
// agent); the remaining configured names follow in config order, forwarded to
// every agent. Every configured name is a secret whatever its suffix.
//
// configured is re-validated with the reader's own validator, so a name that
// bypassed the reader (a future second source) is refused here rather than
// reaching a runtime argv. A failure returns the zero set.
func resolveSecretEnvSet(configured []string) (secretEnvSet, error) {
	if err := validateForwardNames(configured); err != nil {
		return secretEnvSet{}, err
	}

	// A shallow clone is enough: widening replaces a spec's Agents field in the
	// clone and never touches the shared forwardedEnvVarNames literal.
	specs := slices.Clone(forwardedEnvVarNames)
	for _, name := range configured {
		if i := slices.IndexFunc(specs, func(spec secretEnvSpec) bool { return spec.Name == name }); i >= 0 {
			specs[i].Agents = nil
			continue
		}
		specs = append(specs, secretEnvSpec{Name: name})
	}

	set := newSecretEnvSet(specs)
	set.configured = slices.Clone(configured)
	return set, nil
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

// configuredNames returns a copy of the names that came from sandboxEnv.forward,
// in config order. Empty for a built-in-only set.
func (s secretEnvSet) configuredNames() []string {
	return slices.Clone(s.configured)
}
