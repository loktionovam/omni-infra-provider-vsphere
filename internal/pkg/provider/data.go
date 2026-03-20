// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

// Data is the provider custom machine config.
type Data struct {
	Datacenter             string                 `yaml:"datacenter"`
	Cluster                string                 `yaml:"cluster,omitempty"`
	ResourcePool           string                 `yaml:"resource_pool"`
	Datastore              string                 `yaml:"datastore"`
	Network                string                 `yaml:"network"`
	Template               string                 `yaml:"template"`                   // VM template name to clone from
	Folder                 string                 `yaml:"folder"`                     // VM folder path (optional)
	CACert                 string                 `yaml:"ca_cert"`                    // PEM-encoded CA certificate (optional)
	DiskSize               uint64                 `yaml:"disk_size"`                  // GiB
	AdditionalDisks        []uint64               `yaml:"additional_disks,omitempty"` // GiB
	CPU                    uint                   `yaml:"cpu"`
	Memory                 uint                   `yaml:"memory"` // MiB
	PlacementMode          string                 `yaml:"placement_mode,omitempty"`
	PlacementStrategy      string                 `yaml:"placement_strategy,omitempty"`
	SpreadGroup            string                 `yaml:"spread_group,omitempty"`
	HostAntiAffinity       string                 `yaml:"host_anti_affinity,omitempty"`
	HostSelector           HostSelector           `yaml:"host_selector,omitempty"`
	LocalDatastoreSelector LocalDatastoreSelector `yaml:"local_datastore_selector,omitempty"`
	BalancedWeights        BalancedWeights        `yaml:"balanced_weights,omitempty"`
}

// HostSelector filters candidate ESXi hosts for placement.
type HostSelector struct {
	NameRegex          string `yaml:"name_regex,omitempty"`
	RequireConnected   *bool  `yaml:"require_connected,omitempty"`
	ExcludeMaintenance *bool  `yaml:"exclude_maintenance,omitempty"`
}

// LocalDatastoreSelector filters candidate local datastores for placement.
type LocalDatastoreSelector struct {
	NameRegex       string `yaml:"name_regex,omitempty"`
	MinFreeSpaceGiB uint64 `yaml:"min_free_space_gib,omitempty"`
}

// BalancedWeights configures the weighted scoring strategy for auto placement.
type BalancedWeights struct {
	FreeSpace   float64 `yaml:"free_space,omitempty"`
	VMCount     float64 `yaml:"vm_count,omitempty"`
	CPUUsage    float64 `yaml:"cpu_usage,omitempty"`
	MemoryUsage float64 `yaml:"memory_usage,omitempty"`
}
