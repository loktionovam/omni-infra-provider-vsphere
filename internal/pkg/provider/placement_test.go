// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"testing"

	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

func TestSelectPlacementCandidateLeastVMs(t *testing.T) {
	candidates := []placementCandidate{
		{HostName: "esxi-07", DatastoreName: "local-07", VMCount: 7, FreeSpaceGiB: 700},
		{HostName: "esxi-08", DatastoreName: "local-08", VMCount: 2, FreeSpaceGiB: 200},
		{HostName: "esxi-09", DatastoreName: "local-09", VMCount: 4, FreeSpaceGiB: 900},
	}

	best, err := selectPlacementCandidate(candidates, PlacementStrategyLeastVMs, BalancedWeights{})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if best.HostName != "esxi-08" {
		t.Fatalf("expected esxi-08, got %s", best.HostName)
	}
}

func TestSelectPlacementCandidateMostFreeSpace(t *testing.T) {
	candidates := []placementCandidate{
		{HostName: "esxi-07", DatastoreName: "local-07", VMCount: 7, FreeSpaceGiB: 700},
		{HostName: "esxi-08", DatastoreName: "local-08", VMCount: 2, FreeSpaceGiB: 200},
		{HostName: "esxi-09", DatastoreName: "local-09", VMCount: 4, FreeSpaceGiB: 900},
	}

	best, err := selectPlacementCandidate(candidates, PlacementStrategyMostFreeSpace, BalancedWeights{})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if best.HostName != "esxi-09" {
		t.Fatalf("expected esxi-09, got %s", best.HostName)
	}
}

func TestSelectPlacementCandidateBalanced(t *testing.T) {
	candidates := []placementCandidate{
		{HostName: "esxi-07", DatastoreName: "local-07", VMCount: 8, FreeSpaceGiB: 900, CPUUsageRatio: 0.85, MemoryUsageRatio: 0.80},
		{HostName: "esxi-08", DatastoreName: "local-08", VMCount: 3, FreeSpaceGiB: 700, CPUUsageRatio: 0.35, MemoryUsageRatio: 0.25},
		{HostName: "esxi-09", DatastoreName: "local-09", VMCount: 2, FreeSpaceGiB: 500, CPUUsageRatio: 0.75, MemoryUsageRatio: 0.70},
	}

	best, err := selectPlacementCandidate(candidates, PlacementStrategyBalanced, BalancedWeights{
		FreeSpace:   0.45,
		VMCount:     0.30,
		CPUUsage:    0.15,
		MemoryUsage: 0.10,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if best.HostName != "esxi-08" {
		t.Fatalf("expected esxi-08, got %s", best.HostName)
	}
}

func TestNormalizeBalancedWeightsDefaults(t *testing.T) {
	weights := normalizeBalancedWeights(BalancedWeights{})

	expected := normalizeBalancedWeights(defaultBalancedWeights)
	if !nearlyEqual(weights.FreeSpace, expected.FreeSpace) ||
		!nearlyEqual(weights.VMCount, expected.VMCount) ||
		!nearlyEqual(weights.CPUUsage, expected.CPUUsage) ||
		!nearlyEqual(weights.MemoryUsage, expected.MemoryUsage) {
		t.Fatalf("expected default weights %+v, got %+v", expected, weights)
	}
}

func TestMatchesLocalDatastoreSelector(t *testing.T) {
	hostRef := types.ManagedObjectReference{Type: "HostSystem", Value: "host-1"}
	accessible := true
	multipleHostAccess := false

	datastore := mo.Datastore{
		Summary: types.DatastoreSummary{
			Name:               "vts35-local-datastore1",
			Accessible:         true,
			FreeSpace:          int64(200 * GiB),
			MultipleHostAccess: &multipleHostAccess,
		},
		Host: []types.DatastoreHostMount{
			{
				Key: hostRef,
				MountInfo: types.HostMountInfo{
					Accessible: &accessible,
				},
			},
		},
	}

	if !matchesLocalDatastoreSelector(datastore, hostRef, nil, "", 50) {
		t.Fatal("expected datastore to match selector")
	}
}

func TestMatchesHostSelectorRequiresConnectedByDefault(t *testing.T) {
	host := mo.HostSystem{
		Summary: types.HostListSummary{
			Config: types.HostConfigSummary{
				Name: "esxi-07",
			},
			Runtime: &types.HostRuntimeInfo{
				ConnectionState: types.HostSystemConnectionStateDisconnected,
			},
		},
	}

	if matchesHostSelector(host, "esxi-07", nil, HostSelector{}) {
		t.Fatal("expected disconnected host to be rejected by default")
	}
}
