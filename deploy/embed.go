// Package deploy embeds the deployment files that the server distributes:
// the agent install script and its systemd unit.
package deploy

import _ "embed"

// InstallAgent is install-agent.sh; __LUM_SERVER_URL__ and __LUM_AGENT_SERVICE__
// are replaced by the server.
//
//go:embed install-agent.sh
var InstallAgent string

// AgentService is the lum-agent systemd unit.
//
//go:embed lum-agent.service
var AgentService string
