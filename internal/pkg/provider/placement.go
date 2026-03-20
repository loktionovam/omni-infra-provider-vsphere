// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

const (
	PlacementModeExplicit  = "explicit"
	PlacementModeAutoLocal = "auto_local"

	PlacementStrategyLeastVMs      = "least_vms"
	PlacementStrategyMostFreeSpace = "most_free_space"
	PlacementStrategyBalanced      = "balanced"

	HostAntiAffinityPreferred = "preferred"
	HostAntiAffinityRequired  = "required"

	extraConfigSpreadGroupKey = "omni.spread.group"
)

var defaultBalancedWeights = BalancedWeights{
	FreeSpace:   0.45,
	VMCount:     0.30,
	CPUUsage:    0.15,
	MemoryUsage: 0.10,
}

type vmPlacement struct {
	ResourcePoolRef types.ManagedObjectReference
	DatastoreRef    types.ManagedObjectReference
	HostRef         *types.ManagedObjectReference
	HostName        string
	DatastoreName   string
}

type placementCandidate struct {
	HostRef          types.ManagedObjectReference
	HostName         string
	DatastoreRef     types.ManagedObjectReference
	DatastoreName    string
	FreeSpaceGiB     float64
	VMCount          int
	CPUUsageRatio    float64
	MemoryUsageRatio float64
}

func resolveVMPlacement(ctx context.Context, client *govmomi.Client, finder *find.Finder, data Data) (vmPlacement, error) {
	switch normalizePlacementMode(data.PlacementMode) {
	case "", PlacementModeExplicit:
		return resolveExplicitPlacement(ctx, finder, data)
	case PlacementModeAutoLocal:
		return resolveAutoLocalPlacement(ctx, client, finder, data)
	default:
		return vmPlacement{}, fmt.Errorf("unsupported placement_mode %q", data.PlacementMode)
	}
}

func resolveExplicitPlacement(ctx context.Context, finder *find.Finder, data Data) (vmPlacement, error) {
	if strings.TrimSpace(data.ResourcePool) == "" {
		return vmPlacement{}, fmt.Errorf("resource_pool is required when placement_mode is explicit")
	}

	if strings.TrimSpace(data.Datastore) == "" {
		return vmPlacement{}, fmt.Errorf("datastore is required when placement_mode is explicit")
	}

	resourcePool, err := finder.ResourcePool(ctx, data.ResourcePool)
	if err != nil {
		return vmPlacement{}, fmt.Errorf("failed to find resource pool %q: %w", data.ResourcePool, err)
	}

	datastore, err := finder.Datastore(ctx, data.Datastore)
	if err != nil {
		return vmPlacement{}, fmt.Errorf("failed to find datastore %q: %w", data.Datastore, err)
	}

	return vmPlacement{
		ResourcePoolRef: resourcePool.Reference(),
		DatastoreRef:    datastore.Reference(),
		DatastoreName:   data.Datastore,
	}, nil
}

func resolveAutoLocalPlacement(ctx context.Context, client *govmomi.Client, finder *find.Finder, data Data) (vmPlacement, error) {
	if strings.TrimSpace(data.Cluster) == "" {
		return vmPlacement{}, fmt.Errorf("cluster is required when placement_mode is auto_local")
	}

	cluster, err := finder.ClusterComputeResource(ctx, data.Cluster)
	if err != nil {
		return vmPlacement{}, fmt.Errorf("failed to find cluster %q: %w", data.Cluster, err)
	}

	resourcePool, err := resolveAutoLocalResourcePool(ctx, finder, cluster, data.ResourcePool)
	if err != nil {
		return vmPlacement{}, err
	}

	hosts, err := cluster.Hosts(ctx)
	if err != nil {
		return vmPlacement{}, fmt.Errorf("failed to list hosts in cluster %q: %w", data.Cluster, err)
	}

	candidates, err := buildAutoLocalPlacementCandidates(ctx, client, hosts, data)
	if err != nil {
		return vmPlacement{}, err
	}

	candidates, err = applyHostAntiAffinity(ctx, client, hosts, candidates, data)
	if err != nil {
		return vmPlacement{}, err
	}

	candidate, err := selectPlacementCandidate(candidates, normalizePlacementStrategy(data.PlacementStrategy), data.BalancedWeights)
	if err != nil {
		return vmPlacement{}, err
	}

	hostRef := candidate.HostRef

	return vmPlacement{
		ResourcePoolRef: resourcePool.Reference(),
		DatastoreRef:    candidate.DatastoreRef,
		HostRef:         &hostRef,
		HostName:        candidate.HostName,
		DatastoreName:   candidate.DatastoreName,
	}, nil
}

func resolveAutoLocalResourcePool(ctx context.Context, finder *find.Finder, cluster *object.ClusterComputeResource, resourcePoolName string) (*object.ResourcePool, error) {
	resourcePoolName = strings.TrimSpace(resourcePoolName)
	if resourcePoolName == "" || strings.EqualFold(resourcePoolName, "Resources") {
		resourcePool, err := cluster.ResourcePool(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to find default resource pool for cluster: %w", err)
		}

		return resourcePool, nil
	}

	resourcePool, err := finder.ResourcePool(ctx, resourcePoolName)
	if err != nil {
		return nil, fmt.Errorf("failed to find resource pool %q: %w", resourcePoolName, err)
	}

	return resourcePool, nil
}

func buildAutoLocalPlacementCandidates(ctx context.Context, client *govmomi.Client, hosts []*object.HostSystem, data Data) ([]placementCandidate, error) {
	hostRegex, err := compileOptionalRegex(data.HostSelector.NameRegex)
	if err != nil {
		return nil, fmt.Errorf("invalid host_selector.name_regex: %w", err)
	}

	datastoreRegex, err := compileOptionalRegex(data.LocalDatastoreSelector.NameRegex)
	if err != nil {
		return nil, fmt.Errorf("invalid local_datastore_selector.name_regex: %w", err)
	}

	exactDatastoreName := ""
	if datastoreRegex == nil && data.LocalDatastoreSelector.MinFreeSpaceGiB == 0 {
		exactDatastoreName = strings.TrimSpace(data.Datastore)
	}
	requiredFreeSpaceGiB := float64(data.LocalDatastoreSelector.MinFreeSpaceGiB + requestedStorageGiB(data))

	candidates := make([]placementCandidate, 0, len(hosts))

	for _, host := range hosts {
		var hostMO mo.HostSystem

		if err := host.Properties(ctx, host.Reference(), []string{"summary", "vm", "datastore"}, &hostMO); err != nil {
			return nil, fmt.Errorf("failed to get host properties for %q: %w", host.Reference().Value, err)
		}

		hostName := hostMO.Summary.Config.Name
		if !matchesHostSelector(hostMO, hostName, hostRegex, data.HostSelector) {
			continue
		}

		for _, datastoreRef := range hostMO.Datastore {
			datastore := object.NewDatastore(client.Client, datastoreRef)

			var datastoreMO mo.Datastore

			if err := datastore.Properties(ctx, datastoreRef, []string{"summary", "host"}, &datastoreMO); err != nil {
				return nil, fmt.Errorf("failed to get datastore properties for %q: %w", datastoreRef.Value, err)
			}

			if !matchesLocalDatastoreSelector(datastoreMO, host.Reference(), datastoreRegex, exactDatastoreName, requiredFreeSpaceGiB) {
				continue
			}

			candidates = append(candidates, placementCandidate{
				HostRef:          host.Reference(),
				HostName:         hostName,
				DatastoreRef:     datastoreRef,
				DatastoreName:    datastoreMO.Summary.Name,
				FreeSpaceGiB:     bytesToGiB(datastoreMO.Summary.FreeSpace),
				VMCount:          len(hostMO.Vm),
				CPUUsageRatio:    hostCPUUsageRatio(hostMO.Summary),
				MemoryUsageRatio: hostMemoryUsageRatio(hostMO.Summary),
			})
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no suitable host/local datastore candidates found for cluster %q", data.Cluster)
	}

	return candidates, nil
}

func applyHostAntiAffinity(ctx context.Context, client *govmomi.Client, hosts []*object.HostSystem, candidates []placementCandidate, data Data) ([]placementCandidate, error) {
	policy := normalizeHostAntiAffinity(data.HostAntiAffinity)
	if policy == "" {
		return candidates, nil
	}

	spreadGroup := strings.TrimSpace(data.SpreadGroup)
	if spreadGroup == "" {
		return nil, fmt.Errorf("spread_group is required when host_anti_affinity is set")
	}

	hostUsage, err := countSpreadGroupVMsByHost(ctx, client, hosts, spreadGroup)
	if err != nil {
		return nil, err
	}

	filtered, err := applyHostAntiAffinityPolicy(candidates, hostUsage, policy)
	if err != nil {
		return nil, fmt.Errorf("failed to apply host_anti_affinity=%q for spread_group %q: %w", policy, spreadGroup, err)
	}

	return filtered, nil
}

func countSpreadGroupVMsByHost(ctx context.Context, client *govmomi.Client, hosts []*object.HostSystem, spreadGroup string) (map[string]int, error) {
	hostUsage := make(map[string]int, len(hosts))

	for _, host := range hosts {
		var hostMO mo.HostSystem

		if err := host.Properties(ctx, host.Reference(), []string{"vm"}, &hostMO); err != nil {
			return nil, fmt.Errorf("failed to get VM inventory for host %q: %w", host.Reference().Value, err)
		}

		for _, vmRef := range hostMO.Vm {
			vm := object.NewVirtualMachine(client.Client, vmRef)

			var vmMO mo.VirtualMachine

			if err := vm.Properties(ctx, vmRef, []string{"config.extraConfig"}, &vmMO); err != nil {
				return nil, fmt.Errorf("failed to get extra config for VM %q: %w", vmRef.Value, err)
			}

			if vmBelongsToSpreadGroup(vmMO, spreadGroup) {
				hostUsage[host.Reference().Value]++
			}
		}
	}

	return hostUsage, nil
}

func vmBelongsToSpreadGroup(vm mo.VirtualMachine, spreadGroup string) bool {
	if vm.Config == nil {
		return false
	}

	value, ok := extraConfigStringValue(vm.Config.ExtraConfig, extraConfigSpreadGroupKey)

	return ok && value == spreadGroup
}

func extraConfigStringValue(values []types.BaseOptionValue, key string) (string, bool) {
	for _, value := range values {
		optionValue := value.GetOptionValue()
		if optionValue == nil || optionValue.Key != key {
			continue
		}

		switch typedValue := optionValue.Value.(type) {
		case string:
			return typedValue, true
		case nil:
			return "", true
		default:
			return fmt.Sprint(typedValue), true
		}
	}

	return "", false
}

func applyHostAntiAffinityPolicy(candidates []placementCandidate, hostUsage map[string]int, policy string) ([]placementCandidate, error) {
	unusedHostCandidates := filterCandidatesByUnusedHosts(candidates, hostUsage)

	switch policy {
	case HostAntiAffinityPreferred:
		if len(unusedHostCandidates) > 0 {
			return unusedHostCandidates, nil
		}

		return candidates, nil
	case HostAntiAffinityRequired:
		if len(unusedHostCandidates) == 0 {
			return nil, fmt.Errorf("no candidate hosts remain")
		}

		return unusedHostCandidates, nil
	default:
		return nil, fmt.Errorf("unsupported host_anti_affinity %q", policy)
	}
}

func filterCandidatesByUnusedHosts(candidates []placementCandidate, hostUsage map[string]int) []placementCandidate {
	filtered := make([]placementCandidate, 0, len(candidates))

	for _, candidate := range candidates {
		if hostUsage[placementCandidateHostKey(candidate)] > 0 {
			continue
		}

		filtered = append(filtered, candidate)
	}

	return filtered
}

func placementCandidateHostKey(candidate placementCandidate) string {
	if candidate.HostRef.Value != "" {
		return candidate.HostRef.Value
	}

	return candidate.HostName
}

func matchesHostSelector(host mo.HostSystem, hostName string, nameRegex *regexp.Regexp, selector HostSelector) bool {
	if nameRegex != nil && !nameRegex.MatchString(hostName) {
		return false
	}

	if requireConnected(selector) {
		if host.Summary.Runtime == nil || host.Summary.Runtime.ConnectionState != types.HostSystemConnectionStateConnected {
			return false
		}
	}

	if excludeMaintenance(selector) {
		if host.Summary.Runtime != nil && host.Summary.Runtime.InMaintenanceMode {
			return false
		}
	}

	if host.Summary.Runtime != nil && host.Summary.Runtime.InQuarantineMode != nil && *host.Summary.Runtime.InQuarantineMode {
		return false
	}

	return true
}

func matchesLocalDatastoreSelector(datastore mo.Datastore, hostRef types.ManagedObjectReference, nameRegex *regexp.Regexp, exactName string, requiredFreeSpaceGiB float64) bool {
	if !datastore.Summary.Accessible {
		return false
	}

	if !isLocalDatastore(datastore) {
		return false
	}

	if exactName != "" && datastore.Summary.Name != exactName {
		return false
	}

	if nameRegex != nil && !nameRegex.MatchString(datastore.Summary.Name) {
		return false
	}

	if !datastoreAccessibleFromHost(datastore, hostRef) {
		return false
	}

	if bytesToGiB(datastore.Summary.FreeSpace) < requiredFreeSpaceGiB {
		return false
	}

	return true
}

func isLocalDatastore(datastore mo.Datastore) bool {
	if datastore.Summary.MultipleHostAccess != nil && *datastore.Summary.MultipleHostAccess {
		return false
	}

	accessibleMounts := 0

	for _, hostMount := range datastore.Host {
		if hostMount.MountInfo.Accessible == nil || *hostMount.MountInfo.Accessible {
			accessibleMounts++
		}
	}

	return accessibleMounts <= 1
}

func datastoreAccessibleFromHost(datastore mo.Datastore, hostRef types.ManagedObjectReference) bool {
	for _, hostMount := range datastore.Host {
		if hostMount.Key != hostRef {
			continue
		}

		return hostMount.MountInfo.Accessible == nil || *hostMount.MountInfo.Accessible
	}

	return false
}

func requireConnected(selector HostSelector) bool {
	return selector.RequireConnected == nil || *selector.RequireConnected
}

func excludeMaintenance(selector HostSelector) bool {
	return selector.ExcludeMaintenance == nil || *selector.ExcludeMaintenance
}

func compileOptionalRegex(expr string) (*regexp.Regexp, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, nil
	}

	return regexp.Compile(expr)
}

func requestedStorageGiB(data Data) uint64 {
	total := data.DiskSize
	for _, diskSizeGiB := range data.AdditionalDisks {
		total += diskSizeGiB
	}

	return total
}

func bytesToGiB(value int64) float64 {
	return float64(value) / float64(GiB)
}

func hostCPUUsageRatio(summary types.HostListSummary) float64 {
	if summary.Hardware == nil {
		return 1
	}

	totalCPUMHz := float64(summary.Hardware.CpuMhz) * float64(summary.Hardware.NumCpuCores)
	if totalCPUMHz <= 0 {
		return 1
	}

	return clampRatio(float64(summary.QuickStats.OverallCpuUsage) / totalCPUMHz)
}

func hostMemoryUsageRatio(summary types.HostListSummary) float64 {
	if summary.Hardware == nil {
		return 1
	}

	totalMemoryMB := float64(summary.Hardware.MemorySize) / (1024 * 1024)
	if totalMemoryMB <= 0 {
		return 1
	}

	return clampRatio(float64(summary.QuickStats.OverallMemoryUsage) / totalMemoryMB)
}

func clampRatio(value float64) float64 {
	return math.Max(0, math.Min(1, value))
}

func selectPlacementCandidate(candidates []placementCandidate, strategy string, weights BalancedWeights) (placementCandidate, error) {
	if len(candidates) == 0 {
		return placementCandidate{}, fmt.Errorf("no placement candidates available")
	}

	strategy = normalizePlacementStrategy(strategy)

	best := candidates[0]

	for _, candidate := range candidates[1:] {
		switch strategy {
		case PlacementStrategyLeastVMs:
			if betterLeastVMs(candidate, best) {
				best = candidate
			}
		case PlacementStrategyMostFreeSpace:
			if betterMostFreeSpace(candidate, best) {
				best = candidate
			}
		case PlacementStrategyBalanced:
			if betterBalanced(candidate, best, candidates, weights) {
				best = candidate
			}
		default:
			return placementCandidate{}, fmt.Errorf("unsupported placement_strategy %q", strategy)
		}
	}

	return best, nil
}

func betterLeastVMs(a, b placementCandidate) bool {
	if a.VMCount != b.VMCount {
		return a.VMCount < b.VMCount
	}

	return betterTieBreaker(a, b)
}

func betterMostFreeSpace(a, b placementCandidate) bool {
	if !nearlyEqual(a.FreeSpaceGiB, b.FreeSpaceGiB) {
		return a.FreeSpaceGiB > b.FreeSpaceGiB
	}

	if a.VMCount != b.VMCount {
		return a.VMCount < b.VMCount
	}

	return betterTieBreaker(a, b)
}

func betterBalanced(a, b placementCandidate, candidates []placementCandidate, weights BalancedWeights) bool {
	scoreA := balancedPlacementScore(a, candidates, weights)
	scoreB := balancedPlacementScore(b, candidates, weights)

	if !nearlyEqual(scoreA, scoreB) {
		return scoreA > scoreB
	}

	return betterTieBreaker(a, b)
}

func betterTieBreaker(a, b placementCandidate) bool {
	if !nearlyEqual(a.FreeSpaceGiB, b.FreeSpaceGiB) {
		return a.FreeSpaceGiB > b.FreeSpaceGiB
	}

	if a.VMCount != b.VMCount {
		return a.VMCount < b.VMCount
	}

	if !nearlyEqual(a.CPUUsageRatio, b.CPUUsageRatio) {
		return a.CPUUsageRatio < b.CPUUsageRatio
	}

	if !nearlyEqual(a.MemoryUsageRatio, b.MemoryUsageRatio) {
		return a.MemoryUsageRatio < b.MemoryUsageRatio
	}

	if a.HostName != b.HostName {
		return a.HostName < b.HostName
	}

	return a.DatastoreName < b.DatastoreName
}

func balancedPlacementScore(candidate placementCandidate, candidates []placementCandidate, weights BalancedWeights) float64 {
	weights = normalizeBalancedWeights(weights)

	maxFreeSpace := 0.0
	maxVMCount := 0

	for _, other := range candidates {
		maxFreeSpace = math.Max(maxFreeSpace, other.FreeSpaceGiB)
		if other.VMCount > maxVMCount {
			maxVMCount = other.VMCount
		}
	}

	freeSpaceScore := 1.0
	if maxFreeSpace > 0 {
		freeSpaceScore = candidate.FreeSpaceGiB / maxFreeSpace
	}

	vmCountPenalty := 0.0
	if maxVMCount > 0 {
		vmCountPenalty = float64(candidate.VMCount) / float64(maxVMCount)
	}

	return weights.FreeSpace*freeSpaceScore -
		weights.VMCount*vmCountPenalty -
		weights.CPUUsage*candidate.CPUUsageRatio -
		weights.MemoryUsage*candidate.MemoryUsageRatio
}

func normalizeBalancedWeights(weights BalancedWeights) BalancedWeights {
	normalized := BalancedWeights{
		FreeSpace:   math.Max(0, weights.FreeSpace),
		VMCount:     math.Max(0, weights.VMCount),
		CPUUsage:    math.Max(0, weights.CPUUsage),
		MemoryUsage: math.Max(0, weights.MemoryUsage),
	}

	if nearlyEqual(normalized.FreeSpace+normalized.VMCount+normalized.CPUUsage+normalized.MemoryUsage, 0) {
		normalized = defaultBalancedWeights
	}

	sum := normalized.FreeSpace + normalized.VMCount + normalized.CPUUsage + normalized.MemoryUsage
	if sum == 0 {
		return defaultBalancedWeights
	}

	normalized.FreeSpace /= sum
	normalized.VMCount /= sum
	normalized.CPUUsage /= sum
	normalized.MemoryUsage /= sum

	return normalized
}

func normalizePlacementMode(mode string) string {
	return strings.ToLower(strings.TrimSpace(mode))
}

func normalizePlacementStrategy(strategy string) string {
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if strategy == "" {
		return PlacementStrategyBalanced
	}

	return strategy
}

func normalizeHostAntiAffinity(policy string) string {
	return strings.ToLower(strings.TrimSpace(policy))
}

func nearlyEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}
