package service

import (
	"context"
	"time"
)

// ProxmoxStatus is the state of the Proxmox API connection (System status page).
type ProxmoxStatus struct {
	Configured bool   `json:"configured"`
	Version    string `json:"version,omitempty"`
	Error      string `json:"error,omitempty"`
	// MissingLXC lists the containers of the recipes using Proxmox (snapshot
	// or update.resources) that the token cannot see (absent, or no VM.Audit
	// permission).
	MissingLXC []int `json:"missing_lxc,omitempty"`
}

// Proxmox checks the Proxmox API: version, and visibility of the containers to snapshot.
func (s *Service) Proxmox(ctx context.Context) ProxmoxStatus {
	c := s.proxmox.Load()
	if c == nil {
		return ProxmoxStatus{}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st := ProxmoxStatus{Configured: true}
	v, err := c.Version(ctx)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Version = v
	nodes, err := c.Nodes(ctx)
	if err != nil {
		st.Error = err.Error()
		return st
	}
	for _, app := range s.Config().Apps {
		if _, ok := nodes[app.LXCID]; app.NeedsProxmox() && !ok {
			st.MissingLXC = append(st.MissingLXC, app.LXCID)
		}
	}
	return st
}

// logProxmox reports the Proxmox API state at startup.
func (s *Service) logProxmox(ctx context.Context) {
	st := s.Proxmox(ctx)
	switch {
	case !st.Configured:
		var apps []string
		for _, app := range s.Config().Apps {
			if app.NeedsProxmox() {
				apps = append(apps, app.ID)
			}
		}
		if len(apps) > 0 {
			s.logger.Warn("proxmox is not configured: updates of apps with snapshot_before_update or update.resources will be refused", "apps", apps)
		}
	case st.Error != "":
		s.logger.Error("proxmox API unreachable", "error", st.Error)
	case len(st.MissingLXC) > 0:
		s.logger.Warn("proxmox API reachable, but some containers are not visible with the token", "version", st.Version, "missing_lxc", st.MissingLXC)
	default:
		s.logger.Info("proxmox API reachable", "version", st.Version)
	}
}
