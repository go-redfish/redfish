package mockbmc

import (
	"net/http"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The Storage of the mock (DSP8010 Storage 1.x, Drive, Volume 1.x,
// StorageController 1.x; DSP0266 1.23.0 7.11 creating and 7.12 deleting a
// resource): neither mockup carries a Storage resource (both only have
// SimpleStorage), so augmentStorage adds to every system, unless
// Options.SimpleStorageOnly:
//
//   - Storage/RAID1, a RAID controller of the modern shape: a Controllers
//     collection (SupportedRAIDTypes RAID0, RAID1, RAID5), four Drives and a
//     Volumes collection with Vol1 (RAID1 on Disk1 and Disk2) and Vol0 (RAIDType
//     None on Disk3); Disk4 is free;
//   - Storage/AHCI, a controller of the deprecated shape: a StorageControllers
//     array, one drive and an empty Volumes collection, which takes any RAID type.
//
// A POST to a Volumes collection creates a volume if the RAIDType is one the
// controller supports, if Links.Drives names drives of the same Storage that no
// volume uses, and if there are enough of them for the RAID type (RAID1 two,
// RAID5 three); the answer is 201, or 202 with a task when Options.AsyncVolumes
// (the volume exists at once). A DELETE of a volume removes it and frees its drives.
func (b *BMC) augmentStorage() {
	if b.opts.SimpleStorageOnly {
		return
	}
	var systems []string
	for p := range b.res {
		if strings.HasPrefix(p, root+"/Systems/") && strings.Count(p, "/") == 4 {
			systems = append(systems, p)
		}
	}
	sort.Strings(systems)
	status := func() map[string]any { return map[string]any{"State": "Enabled", "Health": "OK"} }
	for _, p := range systems {
		coll := p + "/Storage"
		b.res[coll] = map[string]any{"@odata.type": "#StorageCollection.StorageCollection", "@odata.id": coll, "Name": "Storage Collection", "Members": []any{}, "Members@odata.count": 0}
		b.res[p]["Storage"] = map[string]any{"@odata.id": coll}

		raid := coll + "/RAID1"
		b.addMember(coll, raid, map[string]any{"@odata.type": "#Storage.v1_13_0.Storage", "Id": "RAID1", "Name": "Contoso RAID", "Status": status(),
			"Controllers": map[string]any{"@odata.id": raid + "/Controllers"}, "Volumes": map[string]any{"@odata.id": raid + "/Volumes"},
			"Drives": []any{}, "Drives@odata.count": 0})
		b.res[raid+"/Controllers"] = map[string]any{"@odata.type": "#StorageControllerCollection.StorageControllerCollection", "@odata.id": raid + "/Controllers",
			"Name": "Storage Controller Collection", "Members": []any{}, "Members@odata.count": 0}
		b.addMember(raid+"/Controllers", raid+"/Controllers/1", map[string]any{"@odata.type": "#StorageController.v1_6_0.StorageController", "Id": "1",
			"Name": "Contoso RAID Controller", "Manufacturer": "Contoso", "Model": "R-5000", "SerialNumber": "RC0001", "FirmwareVersion": "2.1.0", "SpeedGbps": 12,
			"SupportedRAIDTypes": []any{"RAID0", "RAID1", "RAID5"}, "Status": status()})
		b.res[raid+"/Volumes"] = map[string]any{"@odata.type": "#VolumeCollection.VolumeCollection", "@odata.id": raid + "/Volumes",
			"Name": "Volume Collection", "Members": []any{}, "Members@odata.count": 0}
		for i := 1; i <= 4; i++ {
			id := "Disk" + strconv.Itoa(i)
			b.addDrive(raid, id, "Contoso SSD "+strconv.Itoa(i), 1_000_000_000_000)
		}
		b.addVolume(raid, "Vol1", "Mirror", "RAID1", 1_000_000_000_000, []string{raid + "/Drives/Disk1", raid + "/Drives/Disk2"})
		b.addVolume(raid, "Vol0", "Pass-through", "None", 1_000_000_000_000, []string{raid + "/Drives/Disk3"})

		ahci := coll + "/AHCI"
		b.addMember(coll, ahci, map[string]any{"@odata.type": "#Storage.v1_13_0.Storage", "Id": "AHCI", "Name": "SATA AHCI", "Status": status(),
			"StorageControllers": []any{map[string]any{"@odata.id": ahci + "#/StorageControllers/0", "MemberId": "0", "Name": "SATA AHCI Controller",
				"Manufacturer": "Contoso", "Model": "A-100", "FirmwareVersion": "1.0", "Status": status()}},
			"Volumes": map[string]any{"@odata.id": ahci + "/Volumes"}, "Drives": []any{}, "Drives@odata.count": 0})
		b.res[ahci+"/Volumes"] = map[string]any{"@odata.type": "#VolumeCollection.VolumeCollection", "@odata.id": ahci + "/Volumes",
			"Name": "Volume Collection", "Members": []any{}, "Members@odata.count": 0}
		b.addDrive(ahci, "Sata1", "SATA disk", 2_000_000_000_000)
	}
}

// addDrive adds a Drive to a Storage resource and lists it in Storage.Drives.
func (b *BMC) addDrive(storage, id, name string, size int64) {
	u := storage + "/Drives/" + id
	b.res[u] = map[string]any{"@odata.type": "#Drive.v1_17_0.Drive", "@odata.id": u, "Id": id, "Name": name, "Manufacturer": "Contoso", "Model": "D-" + id,
		"CapacityBytes": size, "BlockSizeBytes": 512, "MediaType": "SSD", "Protocol": "SAS", "SerialNumber": "SN-" + id,
		"Status": map[string]any{"State": "Enabled", "Health": "OK"}, "Links": map[string]any{"Volumes": []any{}}}
	st := b.res[storage]
	st["Drives"] = append(st["Drives"].([]any), map[string]any{"@odata.id": u})
	st["Drives@odata.count"] = len(st["Drives"].([]any))
}

// addVolume adds a Volume to the Volumes collection of a Storage resource and
// links its drives to it.
func (b *BMC) addVolume(storage, id, name, raid string, size int64, drives []string) {
	coll := storage + "/Volumes"
	links := []any{}
	for _, d := range drives {
		links = append(links, map[string]any{"@odata.id": d})
		dl := b.res[d]["Links"].(map[string]any)
		dl["Volumes"] = append(dl["Volumes"].([]any), map[string]any{"@odata.id": coll + "/" + id})
	}
	b.addMember(coll, coll+"/"+id, map[string]any{"@odata.type": "#Volume.v1_9_0.Volume", "Id": id, "Name": name, "RAIDType": raid, "VolumeType": "Mirrored",
		"CapacityBytes": size, "BlockSizeBytes": 512, "Encrypted": false, "Status": map[string]any{"State": "Enabled", "Health": "OK"},
		"Links": map[string]any{"Drives": links}})
}

// isVolumes reports whether p is a Volumes collection of the mock's Storage.
func (b *BMC) isVolumes(p string) bool {
	c := b.res[p]
	t, _ := c["@odata.type"].(string)
	_, members := c["Members"]
	return members && strings.HasPrefix(t, "#VolumeCollection.")
}

func oneOf(v any, list []any) bool {
	return slices.ContainsFunc(list, func(x any) bool { return x == v })
}

// createVolume serves a POST to a Volumes collection.
func (b *BMC) createVolume(w http.ResponseWriter, r *http.Request, p string) {
	in, err := readJSON(r)
	if err != nil {
		b.fail(w, http.StatusBadRequest, "", "The request body is not a JSON object.", "Base.1.19.MalformedJSON")
		return
	}
	storage := path.Dir(p)
	st := b.res[storage]
	raid, _ := in["RAIDType"].(string)
	if raid == "" {
		b.fail(w, http.StatusBadRequest, "", "The required property RAIDType is missing from the request.", "Base.1.19.PropertyMissing")
		return
	}
	if ctl, _ := st["Controllers"].(map[string]any); ctl != nil {
		cu, _ := ctl["@odata.id"].(string)
		if ms, _ := b.res[cu]["Members"].([]any); len(ms) > 0 {
			first, _ := ms[0].(map[string]any)
			if sup, _ := b.res[first["@odata.id"].(string)]["SupportedRAIDTypes"].([]any); sup != nil && !oneOf(raid, sup) {
				b.fail(w, http.StatusBadRequest, "", "The value "+raid+" for the property RAIDType is not in the list of acceptable values.", "Base.1.19.PropertyValueNotInList")
				return
			}
		}
	}
	links, _ := in["Links"].(map[string]any)
	list, _ := links["Drives"].([]any)
	if len(list) == 0 {
		b.fail(w, http.StatusBadRequest, "", "The required property Links/Drives is missing from the request.", "Base.1.19.PropertyMissing")
		return
	}
	var drives []string
	var sizes []int64
	own, _ := st["Drives"].([]any)
	for _, x := range list {
		m, _ := x.(map[string]any)
		d, _ := m["@odata.id"].(string)
		if !slices.ContainsFunc(own, func(y any) bool { ym, _ := y.(map[string]any); return ym["@odata.id"] == d }) {
			b.fail(w, http.StatusBadRequest, "", "The drive "+d+" is not a drive of this storage subsystem.", "Base.1.19.ResourceMissingAtURI")
			return
		}
		if slices.Contains(drives, d) {
			b.fail(w, http.StatusBadRequest, "", "The drive "+d+" is listed twice.", "Base.1.19.PropertyValueConflict")
			return
		}
		if vols, _ := b.res[d]["Links"].(map[string]any)["Volumes"].([]any); len(vols) > 0 {
			b.fail(w, http.StatusConflict, "", "The drive "+d+" is already used by a volume.", "Base.1.19.ResourceInUse")
			return
		}
		drives = append(drives, d)
		n, _ := b.res[d]["CapacityBytes"].(int64)
		sizes = append(sizes, n)
	}
	need := map[string]int{"RAID1": 2, "RAID5": 3}[raid]
	if need == 0 {
		need = 1
	}
	if len(drives) < need {
		b.fail(w, http.StatusBadRequest, "", raid+" needs at least "+strconv.Itoa(need)+" drives.", "Base.1.19.PropertyValueConflict")
		return
	}
	smallest, sum := sizes[0], int64(0)
	for _, n := range sizes {
		smallest, sum = min64(smallest, n), sum+n
	}
	capacity := map[string]int64{"RAID0": sum, "RAID5": smallest * int64(len(sizes)-1)}[raid]
	if capacity == 0 {
		capacity = smallest
	}
	if v, ok := in["CapacityBytes"]; ok {
		n, isNum := v.(float64)
		if !isNum || n <= 0 || int64(n) > capacity {
			b.fail(w, http.StatusBadRequest, "", "The value for the property CapacityBytes is not valid for this RAID type and these drives.", "Base.1.19.PropertyValueIncorrect")
			return
		}
		capacity = int64(n)
	}
	id := "Vol" + strconv.Itoa(len(b.res[p]["Members"].([]any))+1)
	for b.res[p+"/"+id] != nil {
		id += "x"
	}
	name, _ := in["Name"].(string)
	if name == "" {
		name = id
	}
	b.addVolume(storage, id, name, raid, capacity, drives)
	vp := p + "/" + id
	if b.opts.AsyncVolumes {
		b.newTask(w)
		return
	}
	w.Header().Set("Location", vp)
	writeJSON(w, http.StatusCreated, b.res[vp], etagOf(b.res[vp]))
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// deleteVolume serves a DELETE of a volume.
func (b *BMC) deleteVolume(w http.ResponseWriter, p string) {
	m, ok := b.res[p]
	if !ok {
		b.fail(w, http.StatusNotFound, "", "The requested resource was not found.", "Base.1.19.ResourceNotFound")
		return
	}
	coll := path.Dir(p)
	c := b.res[coll]
	var keep []any
	for _, x := range c["Members"].([]any) {
		if x.(map[string]any)["@odata.id"] != p {
			keep = append(keep, x)
		}
	}
	c["Members"], c["Members@odata.count"] = keep, len(keep)
	links, _ := m["Links"].(map[string]any)
	list, _ := links["Drives"].([]any)
	for _, x := range list {
		d, _ := x.(map[string]any)["@odata.id"].(string)
		if dl, _ := b.res[d]["Links"].(map[string]any); dl != nil {
			var rest []any
			for _, v := range dl["Volumes"].([]any) {
				if v.(map[string]any)["@odata.id"] != p {
					rest = append(rest, v)
				}
			}
			if rest == nil {
				rest = []any{}
			}
			dl["Volumes"] = rest
		}
	}
	delete(b.res, p)
	if b.opts.AsyncVolumes {
		b.newTask(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
