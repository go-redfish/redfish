package redfish_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-redfish/redfish"
	"github.com/go-redfish/redfish/mockbmc"
)

const raidPath = sysPath + "/Storage/RAID1"

func TestStorageInventories(t *testing.T) {
	forEachTree(t, func(t *testing.T, _ *mockbmc.BMC, c *redfish.Client, sys string) {
		ctx := context.Background()
		ctl, err := c.StorageControllerInventory(ctx, sys)
		if err != nil || len(ctl) != 2 {
			t.Fatalf("controllers = %v, %v", ctl, err)
		}
		// The modern Controllers collection, then the deprecated StorageControllers array.
		if ctl[0]["Name"] != "Contoso RAID Controller" || ctl[0]["FirmwareVersion"] != "2.1.0" || ctl[1]["Name"] != "SATA AHCI Controller" {
			t.Fatalf("controllers = %v", ctl)
		}
		if _, ok := ctl[0]["SupportedRAIDTypes"]; ok {
			t.Fatal("a property outside the oracle's list was kept")
		}

		disks, err := c.DiskInventory(ctx, sys)
		if err != nil || len(disks) != 2 {
			t.Fatalf("disks = %v, %v", disks, err)
		}
		if disks[0]["Controller"] != "Contoso RAID Controller" || disks[0]["StorageId"] != "RAID1" {
			t.Fatalf("disks[0] = %v", disks[0])
		}
		drives := disks[0]["Drives"].([]any)
		d1 := drives[0].(redfish.Props)
		vols, _ := d1["Volumes"].([]any)
		if len(drives) != 4 || d1["RedfishURI"] != raidPath+"/Drives/Disk1" || len(vols) != 1 || vols[0] != raidPath+"/Volumes/Vol1" {
			t.Fatalf("drives = %v", drives)
		}
		if _, ok := d1["Links"]; ok {
			t.Fatal("Links is reported as Volumes, not as Links")
		}

		v, err := c.VolumeInventory(ctx, sys)
		if err != nil || len(v) != 2 {
			t.Fatalf("volumes = %v, %v", v, err)
		}
		vv := v[0]["Volumes"].([]any)
		v1 := vv[0].(redfish.Props)
		ld, _ := v1["Linked_drives"].([]any)
		if v[0]["Controller"] != "Contoso RAID Controller" || len(vv) != 2 || v1["RAIDType"] != "RAID1" || len(ld) != 2 || ld[0].(map[string]any)["Id"] != "Disk1" {
			t.Fatalf("volumes[0] = %v", v[0])
		}
		if len(v[1]["Volumes"].([]any)) != 0 || v[1]["Controller"] != "SATA AHCI Controller" {
			t.Fatalf("volumes[1] = %v", v[1])
		}
	})
}

func TestSimpleStorageOnly(t *testing.T) {
	for _, tree := range trees {
		t.Run(tree, func(t *testing.T) {
			_, srv := newMock(t, mockbmc.Options{Tree: tree, SimpleStorageOnly: true})
			c := newClient(t, srv, redfish.Config{})
			ctx := context.Background()
			disks, err := c.DiskInventory(ctx, sysPath)
			if err != nil || len(disks) != 1 || disks[0]["Controller"] != "Simple Storage Controller" {
				t.Fatalf("disks = %v, %v", disks, err)
			}
			if d := disks[0]["Drives"].([]any); len(d) != 4 || d[0].(redfish.Props)["Name"] != "SATA Bay 1" {
				t.Fatalf("devices = %v", d)
			}
			// controllers and volumes need Storage: the oracle's message.
			for name, fn := range map[string]func() error{
				"controllers": func() error { _, err := c.StorageControllerInventory(ctx, sysPath); return err },
				"volumes":     func() error { _, err := c.VolumeInventory(ctx, sysPath); return err },
			} {
				if err := fn(); !errors.Is(err, redfish.ErrNotFound) || !strings.Contains(err.Error(), "Storage resource not found") {
					t.Fatalf("%s: %v", name, err)
				}
			}
		})
	}
}

func TestCreateAndDeleteVolume(t *testing.T) {
	forEachTree(t, func(t *testing.T, b *mockbmc.BMC, c *redfish.Client, sys string) {
		ctx := context.Background()
		d := func(n string) string { return raidPath + "/Drives/" + n }
		create := func(raid string, drives ...string) (redfish.Outcome, error) {
			return c.CreateVolume(ctx, sys, "RAID1", redfish.VolumeRequest{Details: redfish.Props{"RAIDType": raid, "Name": "data"}, Drives: drives})
		}

		// Refused before the request: a RAID type the controller does not support,
		// a drive of another subsystem.
		if _, err := create("RAID10", d("Disk4")); !errors.Is(err, redfish.ErrMismatch) {
			t.Fatalf("RAID10: %v", err)
		}
		if _, err := create("RAID0", sysPath+"/Storage/AHCI/Drives/Sata1"); !errors.Is(err, redfish.ErrMismatch) {
			t.Fatalf("a foreign drive: %v", err)
		}
		if _, err := c.CreateVolume(ctx, sys, "RAID1", redfish.VolumeRequest{Details: redfish.Props{"Name": "x"}, Drives: []string{d("Disk4")}}); err == nil || !strings.Contains(err.Error(), "required") {
			t.Fatalf("no RAIDType: %v", err)
		}
		if _, err := c.CreateVolume(ctx, sys, "RAID1", redfish.VolumeRequest{Details: redfish.Props{"RAIDType": "RAID0"}}); err == nil {
			t.Fatal("no drives accepted")
		}
		if _, err := c.CreateVolume(ctx, sys, "NOPE", redfish.VolumeRequest{Details: redfish.Props{"RAIDType": "RAID0"}, Drives: []string{d("Disk4")}}); !errors.Is(err, redfish.ErrNotFound) {
			t.Fatalf("unknown subsystem: %v", err)
		}
		// The service's own refusals: a drive in use (409), too few drives (400).
		var se *redfish.StatusError
		if _, err := create("RAID0", d("Disk1")); !errors.As(err, &se) || se.Code != 409 {
			t.Fatalf("a drive in use: %v", err)
		}
		if _, err := create("RAID1", d("Disk4")); !errors.As(err, &se) || se.Code != 400 {
			t.Fatalf("RAID1 on one drive: %v", err)
		}

		o, err := create("RAID0", d("Disk4"))
		if err != nil || !o.Changed || o.Message != "Volume Created" || o.Monitor != "" {
			t.Fatalf("create = %+v, %v", o, err)
		}
		made := resourceOf(t, b, raidPath+"/Volumes/Vol3")
		if made["RAIDType"] != "RAID0" || made["Name"] != "data" {
			t.Fatalf("created %v", made)
		}
		if _, err := create("RAID0", d("Disk4")); !errors.As(err, &se) || se.Code != 409 {
			t.Fatalf("the drive is used now: %v", err)
		}

		// DeleteNoneVolumes removes Vol0 (RAIDType None) first and says which.
		var gone []string
		o, err = c.CreateVolume(ctx, sys, "RAID1", redfish.VolumeRequest{Details: redfish.Props{"RAIDType": "RAID0"}, Drives: []string{d("Disk3")},
			DeleteNoneVolumes: true, OnDelete: func(u []string) { gone = u }})
		if err != nil || !o.Changed || len(gone) != 1 || gone[0] != raidPath+"/Volumes/Vol0" {
			t.Fatalf("create with DeleteNoneVolumes = %+v, %v, %v", o, err, gone)
		}
		if b.Resource(raidPath+"/Volumes/Vol0") != nil {
			t.Fatal("Vol0 still there")
		}

		// delete: unknown id refuses everything, none named refuses, dry run deletes nothing.
		if _, err := c.DeleteVolumes(ctx, sys, "RAID1", nil, redfish.DeleteVolumesOptions{}); err == nil {
			t.Fatal("no volume named")
		}
		if _, err := c.DeleteVolumes(ctx, sys, "RAID1", []string{"Vol1", "Nope"}, redfish.DeleteVolumesOptions{}); !errors.Is(err, redfish.ErrNotFound) {
			t.Fatalf("unknown volume: %v", err)
		}
		if b.Resource(raidPath+"/Volumes/Vol1") == nil {
			t.Fatal("Vol1 was deleted although another id was unknown")
		}
		var plan []string
		o, err = c.DeleteVolumes(ctx, sys, "RAID1", []string{"Vol1"}, redfish.DeleteVolumesOptions{DryRun: true, OnPlan: func(u []string) { plan = u }})
		if err != nil || o.Changed || len(plan) != 1 || b.Resource(raidPath+"/Volumes/Vol1") == nil {
			t.Fatalf("dry run = %+v, %v, %v", o, err, plan)
		}
		o, err = c.DeleteVolumes(ctx, sys, "RAID1", []string{"Vol1", "Vol3", "Vol1"}, redfish.DeleteVolumesOptions{})
		if err != nil || !o.Changed || b.Resource(raidPath+"/Volumes/Vol1") != nil || b.Resource(raidPath+"/Volumes/Vol3") != nil {
			t.Fatalf("delete = %+v, %v", o, err)
		}
		// its drives are free again
		if _, err := create("RAID1", d("Disk1"), d("Disk2")); err != nil {
			t.Fatalf("drives not freed: %v", err)
		}
		// an empty collection is not an error (the oracle: changed false)
		o, err = c.DeleteVolumes(ctx, sys, "AHCI", []string{"x"}, redfish.DeleteVolumesOptions{})
		if err != nil || o.Changed {
			t.Fatalf("empty collection = %+v, %v", o, err)
		}
	})
}

func TestVolumeTasks(t *testing.T) {
	_, srv := newMock(t, mockbmc.Options{AsyncVolumes: true})
	c := newClient(t, srv, redfish.Config{})
	ctx := context.Background()
	o, err := c.CreateVolume(ctx, sysPath, "RAID1", redfish.VolumeRequest{Details: redfish.Props{"RAIDType": "RAID0"}, Drives: []string{raidPath + "/Drives/Disk4"}})
	if err != nil || o.Monitor == "" || len(o.Monitors) != 1 {
		t.Fatalf("create = %+v, %v", o, err)
	}
	if _, err := c.WaitTask(ctx, o.Monitor, 0, nil); err != nil {
		t.Fatal(err)
	}
	o, err = c.DeleteVolumes(ctx, sysPath, "RAID1", []string{"Vol1", "Vol3"}, redfish.DeleteVolumesOptions{})
	if err != nil || len(o.Monitors) != 2 || o.Monitor != o.Monitors[0] {
		t.Fatalf("delete = %+v, %v", o, err)
	}
}

func TestBuildVolumePayload(t *testing.T) {
	p, err := redfish.BuildVolumePayload(redfish.Props{"RAIDType": "RAID1", "Name": "n"}, []string{"/redfish/v1/a", "http://h/redfish/v1/b"})
	if err != nil {
		t.Fatal(err)
	}
	links := p["Links"].(map[string]any)["Drives"].([]any)
	if _, ok := p["Drives"]; ok || len(links) != 2 || links[1].(map[string]any)["@odata.id"] != "/redfish/v1/b" || p["Name"] != "n" {
		t.Fatalf("payload = %v", p)
	}
	for _, bad := range [][]string{{"/etc/passwd"}, {"/redfish/../x"}, {}} {
		if _, err := redfish.BuildVolumePayload(redfish.Props{"RAIDType": "RAID0"}, bad); err == nil {
			t.Fatalf("drives %v accepted", bad)
		}
	}
	if _, err := redfish.BuildVolumePayload(redfish.Props{"RAIDType": "RAID0", "Links": 1}, []string{"/redfish/v1/a"}); err == nil {
		t.Fatal("a Links detail was accepted")
	}
}
