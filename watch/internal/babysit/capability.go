package babysit

import "fmt"

// ArmWorkflows lists the built-in workflows an agent must have before babysit
// may arm supervision with it (#1215). merge-repair is excluded: it is pinned to
// Claude regardless of the arming agent.
var ArmWorkflows = []string{"address-review", "babysit-attention", "ci-repair"}

// CapabilityRefusal returns the reason agent may not arm babysit, or "" when
// it may. The reason names the first workflow in ArmWorkflows the agent lacks.
// The predicate is injected because this package cannot import internal/run
// (import cycle through internal/daemon); a nil predicate counts as no
// capability, so the default refuses.
func CapabilityRefusal(agent string, has func(agent, workflow string) bool) string {
	for _, workflow := range ArmWorkflows {
		if has == nil || !has(agent, workflow) {
			return fmt.Sprintf("agent %q has no built-in %q workflow", agent, workflow)
		}
	}
	return ""
}
