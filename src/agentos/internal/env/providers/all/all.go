// Package all links every environment provider into the binary (blank
// imports run their init() registration). Provider owners add their import
// here (one line each).
package all

import (
	_ "github.com/agentos/agentos/internal/env/bootstrap" // env.Installer for install-agentos
	_ "github.com/agentos/agentos/internal/env/providers/agentoslocal"
	_ "github.com/agentos/agentos/internal/env/providers/agentosremote"
	_ "github.com/agentos/agentos/internal/env/providers/docker"
	_ "github.com/agentos/agentos/internal/env/providers/hyperv"
	_ "github.com/agentos/agentos/internal/env/providers/ssh"
)
