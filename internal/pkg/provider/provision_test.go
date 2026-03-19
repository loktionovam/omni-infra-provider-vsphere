// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package provider

import (
	"testing"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/types"
)

func TestBuildAdditionalDiskDevicesCreatesControllerAndDisks(t *testing.T) {
	datastoreRef := types.ManagedObjectReference{Type: "Datastore", Value: "datastore-1"}

	addDevices, err := buildAdditionalDiskDevices(nil, datastoreRef, []uint64{20, 50})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(addDevices) != 3 {
		t.Fatalf("expected controller plus 2 disks, got %d devices", len(addDevices))
	}

	controller, ok := addDevices[0].(types.BaseVirtualController)
	if !ok {
		t.Fatalf("expected first device to be a controller, got %T", addDevices[0])
	}

	firstDisk, ok := addDevices[1].(*types.VirtualDisk)
	if !ok {
		t.Fatalf("expected second device to be a virtual disk, got %T", addDevices[1])
	}

	secondDisk, ok := addDevices[2].(*types.VirtualDisk)
	if !ok {
		t.Fatalf("expected third device to be a virtual disk, got %T", addDevices[2])
	}

	if firstDisk.ControllerKey != controller.GetVirtualController().Key {
		t.Fatalf("expected first disk controller key %d, got %d", controller.GetVirtualController().Key, firstDisk.ControllerKey)
	}

	if secondDisk.ControllerKey != controller.GetVirtualController().Key {
		t.Fatalf("expected second disk controller key %d, got %d", controller.GetVirtualController().Key, secondDisk.ControllerKey)
	}

	if firstDisk.CapacityInBytes != int64(20*GiB) {
		t.Fatalf("expected first disk capacity %d, got %d", int64(20*GiB), firstDisk.CapacityInBytes)
	}

	if secondDisk.CapacityInBytes != int64(50*GiB) {
		t.Fatalf("expected second disk capacity %d, got %d", int64(50*GiB), secondDisk.CapacityInBytes)
	}

	if firstDisk.UnitNumber == nil || *firstDisk.UnitNumber != 0 {
		t.Fatalf("expected first disk unit number 0, got %#v", firstDisk.UnitNumber)
	}

	if secondDisk.UnitNumber == nil || *secondDisk.UnitNumber != 1 {
		t.Fatalf("expected second disk unit number 1, got %#v", secondDisk.UnitNumber)
	}
}

func TestBuildAdditionalDiskDevicesReusesExistingController(t *testing.T) {
	datastoreRef := types.ManagedObjectReference{Type: "Datastore", Value: "datastore-1"}
	devices := object.VirtualDeviceList{}

	controllerDevice, err := devices.CreateSCSIController("pvscsi")
	if err != nil {
		t.Fatalf("failed to create controller: %v", err)
	}

	devices = append(devices, controllerDevice)

	addDevices, err := buildAdditionalDiskDevices(devices, datastoreRef, []uint64{20})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(addDevices) != 1 {
		t.Fatalf("expected one disk to be added, got %d devices", len(addDevices))
	}

	if _, ok := addDevices[0].(*types.VirtualDisk); !ok {
		t.Fatalf("expected added device to be a virtual disk, got %T", addDevices[0])
	}
}

func TestBuildAdditionalDiskDevicesCreatesNewControllerWhenExistingIsFull(t *testing.T) {
	datastoreRef := types.ManagedObjectReference{Type: "Datastore", Value: "datastore-1"}
	devices := object.VirtualDeviceList{}

	controllerDevice, err := devices.CreateSCSIController("pvscsi")
	if err != nil {
		t.Fatalf("failed to create controller: %v", err)
	}

	devices = append(devices, controllerDevice)

	controller, ok := controllerDevice.(types.BaseVirtualController)
	if !ok {
		t.Fatalf("expected controller device, got %T", controllerDevice)
	}

	for range 15 {
		disk := devices.CreateDisk(controller, datastoreRef, "")
		devices = append(devices, disk)
	}

	addDevices, err := buildAdditionalDiskDevices(devices, datastoreRef, []uint64{20})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(addDevices) != 2 {
		t.Fatalf("expected new controller plus disk, got %d devices", len(addDevices))
	}

	if _, ok := addDevices[0].(types.BaseVirtualController); !ok {
		t.Fatalf("expected first added device to be a controller, got %T", addDevices[0])
	}

	if _, ok := addDevices[1].(*types.VirtualDisk); !ok {
		t.Fatalf("expected second added device to be a disk, got %T", addDevices[1])
	}
}

func TestBuildAdditionalDiskDevicesRejectsZeroSizedDisk(t *testing.T) {
	datastoreRef := types.ManagedObjectReference{Type: "Datastore", Value: "datastore-1"}

	if _, err := buildAdditionalDiskDevices(nil, datastoreRef, []uint64{0}); err == nil {
		t.Fatal("expected an error for a zero-sized additional disk")
	}
}
