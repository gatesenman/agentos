package ssh

import "github.com/agentos/agentos/internal/env"

// CapabilitiesFor: lifecycle (start/stop) only when the matching commands
// are configured. Delete is always allowed by the server (it only forgets
// the host).
func (p provider) CapabilitiesFor(rec *env.Record) []env.Capability {
	caps := p.Info().Capabilities
	if c, err := parseConfig(rec.Config); err != nil || (c.StartCommand == "" && c.StopCommand == "") {
		caps = env.Without(caps, env.CapLifecycle)
	}
	return caps
}

var _ env.CapabilityReporter = provider{}
