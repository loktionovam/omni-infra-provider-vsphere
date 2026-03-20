// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/vmware/govmomi/find"
)

// HealthChecker validates provider reachability to vCenter.
type HealthChecker struct {
	provisioner *Provisioner
}

// NewHealthChecker creates a new health checker for the provider.
func NewHealthChecker(provisioner *Provisioner) *HealthChecker {
	return &HealthChecker{
		provisioner: provisioner,
	}
}

// Check validates vCenter connectivity and basic inventory reachability.
func (h *HealthChecker) Check(ctx context.Context) error {
	sessionCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := h.provisioner.ensureSession(sessionCtx); err != nil {
		return fmt.Errorf("vcenter session check failed: %w", err)
	}

	if _, err := h.provisioner.vsphereClient.SessionManager.UserSession(sessionCtx); err != nil {
		return fmt.Errorf("vcenter user session check failed: %w", err)
	}

	finder := find.NewFinder(h.provisioner.vsphereClient.Client, true)

	datacenters, err := finder.DatacenterList(sessionCtx, "*")
	if err != nil {
		return fmt.Errorf("vcenter datacenter discovery failed: %w", err)
	}

	if len(datacenters) == 0 {
		return fmt.Errorf("vcenter datacenter discovery returned no datacenters")
	}

	return nil
}
