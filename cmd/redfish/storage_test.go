package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-redfish/redfish/mockbmc"
)

const cliRaid = "/redfish/v1/Systems/437XR1138R2/Storage/RAID1"

func jsonList(t *testing.T, s string) []map[string]any {
	t.Helper()
	var l []map[string]any
	if err := json.Unmarshal([]byte(s), &l); err != nil {
		t.Fatalf("not a JSON array: %v\n%s", err, s)
	}
	return l
}

func TestStorageCommands(t *testing.T) {
	for _, tree := range []string{mockbmc.TreeRackmount, mockbmc.TreeEmulator} {
		t.Run(tree, func(t *testing.T) {
			e := newEnvTree(t, tree)
			out, _, err := e.run("storage", "controllers")
			if l := jsonList(t, out); err != nil || len(l) != 2 || l[0]["Name"] != "Contoso RAID Controller" {
				t.Fatalf("controllers: %v\n%s", err, out)
			}
			out, _, err = e.run("storage", "disks", "-o", "table")
			if err != nil || !strings.Contains(out, "Contoso RAID Controller") {
				t.Fatalf("disks: %v\n%s", err, out)
			}
			out, _, err = e.run("storage", "volumes")
			if l := jsonList(t, out); err != nil || len(l) != 2 {
				t.Fatalf("volumes: %v\n%s", err, out)
			}
			if _, _, err := e.run("storage", "controllers", "--id", "nope"); err == nil {
				t.Fatal("an unknown system was accepted")
			}
		})
	}
}

func TestStorageVolumeCreateDelete(t *testing.T) {
	e := newEnv(t)
	d := func(n string) string { return cliRaid + "/Drives/" + n }

	// Required flags and the RAID check are refused before anything changes.
	if _, _, err := e.run("storage", "volume", "create", "--storage", "RAID1", "--drive", d("Disk4")); err == nil {
		t.Fatal("create without --raid-type")
	}
	if _, _, err := e.run("storage", "volume", "create", "--storage", "RAID1", "--raid-type", "RAID10", "--drive", d("Disk4")); err == nil || !strings.Contains(err.Error(), "SupportedRAIDTypes") {
		t.Fatalf("RAID10: %v", err)
	}
	out, errOut, err := e.run("storage", "volume", "create", "--storage", "RAID1", "--raid-type", "RAID0", "--drive", d("Disk4"), "--name", "scratch", "--capacity-bytes", "500000000000")
	if err != nil || jsonOut(t, out)["message"] != "Volume Created" {
		t.Fatalf("create: %v\n%s\n%s", err, out, errOut)
	}
	if v := e.bmc.Resource(cliRaid + "/Volumes/Vol3"); !strings.Contains(string(v), `"scratch"`) || !strings.Contains(string(v), "500000000000") {
		t.Fatalf("created %s", v)
	}

	// create with --delete-none-volumes names what it deletes.
	out, errOut, err = e.run("storage", "volume", "create", "--storage", "RAID1", "--raid-type", "RAID0", "--drive", d("Disk3"), "--delete-none-volumes")
	if err != nil || !strings.Contains(errOut, cliRaid+"/Volumes/Vol0") {
		t.Fatalf("create --delete-none-volumes: %v\n%s\n%s", err, out, errOut)
	}

	// delete needs names, a storage, and prints what it deletes.
	if _, _, err := e.run("storage", "volume", "delete", "--storage", "RAID1"); err == nil || !strings.Contains(err.Error(), "no delete-all") {
		t.Fatalf("delete without names: %v", err)
	}
	if _, _, err := e.run("storage", "volume", "delete", "Vol1"); err == nil {
		t.Fatal("delete without --storage")
	}
	if _, _, err := e.run("storage", "volume", "delete", "--storage", "RAID1", "Vol1", "Nope"); err == nil || e.bmc.Resource(cliRaid+"/Volumes/Vol1") == nil {
		t.Fatalf("an unknown name: %v", err)
	}
	_, errOut, err = e.run("storage", "volume", "delete", "--storage", "RAID1", "--dry-run", "Vol1")
	if err != nil || !strings.Contains(errOut, "would delete "+cliRaid+"/Volumes/Vol1") || e.bmc.Resource(cliRaid+"/Volumes/Vol1") == nil {
		t.Fatalf("dry run: %v\n%s", err, errOut)
	}
	out, errOut, err = e.run("storage", "volume", "delete", "--storage", "RAID1", "Vol1")
	if err != nil || !strings.Contains(errOut, "deleting "+cliRaid+"/Volumes/Vol1") || jsonOut(t, out)["changed"] != true || e.bmc.Resource(cliRaid+"/Volumes/Vol1") != nil {
		t.Fatalf("delete: %v\n%s\n%s", err, out, errOut)
	}
	if e.bmc.Resource(cliRaid+"/Volumes/Vol3") == nil {
		t.Fatal("a volume that was not named was deleted")
	}
}

func TestStorageVolumeTasks(t *testing.T) {
	e := newEnvOpts(t, mockbmc.Options{AsyncVolumes: true})
	_, errOut, err := e.run("storage", "volume", "delete", "--storage", "RAID1", "Vol1", "Vol0")
	if err != nil || strings.Count(errOut, "monitor: /redfish/v1/TaskService/Tasks/") != 2 {
		t.Fatalf("delete: %v\n%s", err, errOut)
	}
}
