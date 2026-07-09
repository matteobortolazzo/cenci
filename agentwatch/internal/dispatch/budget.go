package dispatch

// Budget is the remaining headroom for one agent. Unlimited short-circuits the
// budget gate; otherwise Remaining <= 0 means exhausted.
type Budget struct {
	Agent     string
	Remaining float64
	Unlimited bool
}

// BudgetProvider resolves the current budget for an agent. It is the seam for
// #77's real per-agent usage accounting.
type BudgetProvider interface {
	Budget(agent string) Budget
}

// FloorProvider is the #45 stub: it performs no real usage reads. An agent is
// Unlimited unless a config floor pins it, in which case the floor value is its
// remaining headroom (a floor of 0 exhausts it). #77 replaces this with ccusage
// parsing.
type FloorProvider struct {
	Floors map[string]float64
}

// Budget returns the configured floor as remaining headroom, or Unlimited when
// no floor pins the agent.
func (p FloorProvider) Budget(agent string) Budget {
	if v, ok := p.Floors[agent]; ok {
		return Budget{Agent: agent, Remaining: v}
	}
	return Budget{Agent: agent, Unlimited: true}
}
