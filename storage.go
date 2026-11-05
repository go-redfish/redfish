package redfish

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// The Storage commands of community.general (33e521d6): get_storage_controller_inventory,
// get_disk_inventory, get_volume_inventory (redfish_info), create_volume and
// delete_volumes (redfish_config), read from redfish_utils.py. The resources are
// Storage 1.x (Controllers, the deprecated StorageControllers, Drives, Volumes),
// SimpleStorage 1.x (Devices), Drive, Volume 1.x (RAIDType, Links.Drives) and
// StorageController 1.x (SupportedRAIDTypes) of DSP8010; the protocol rules are
// DSP0266 1.23.0: creating a resource by POST to its collection 7.11 (201, or
// 202 with a task monitor 12.2) and deleting one 7.12.

// maxStorageReads bounds the resources one storage command reads.
const maxStorageReads = 1024

var (
	controllerProperties = []string{"CacheSummary", "FirmwareVersion", "Identifiers", "Location", "Manufacturer", "Model", "Name", "Id", "PartNumber", "SerialNumber", "SpeedGbps", "Status"}
	driveProperties      = []string{"BlockSizeBytes", "CapableSpeedGbs", "CapacityBytes", "EncryptionAbility", "EncryptionStatus", "FailurePredicted", "HotspareType", "Id", "Identifiers", "Links", "Manufacturer", "MediaType", "Model", "Name", "PartNumber", "PhysicalLocation", "Protocol", "Revision", "RotationSpeedRPM", "SerialNumber", "Status"}
	volumeProperties     = []string{"Id", "Name", "RAIDType", "VolumeType", "BlockSizeBytes", "Capacity", "CapacityBytes", "CapacitySources", "Encrypted", "EncryptionTypes", "Identifiers", "Operations", "OptimumIOSizeBytes", "AccessCapabilities", "AllocatedPools", "Status"}
)

// lastSegment is the last segment of a path (the Id a link ends with).
func lastSegment(p string) string {
	p = strings.TrimRight(p, "/")
	return p[strings.LastIndexByte(p, '/')+1:]
}

// pickSet keeps the named properties a resource has that are not null (the
// oracle's drive and volume lists).
func pickSet(p Props, keys []string) Props {
	out := Props{}
	for _, k := range keys {
		if v, ok := p[k]; ok && v != nil {
			out[k] = v
		}
	}
	return out
}

// ParseController keeps the inventory properties of a storage controller (a
// StorageController resource or an element of Storage.StorageControllers).
func ParseController(data []byte) (Props, error) {
	return Pick(data, controllerProperties...)
}

// ParseDrive decodes a Drive: its inventory properties, RedfishURI (its
// @odata.id, else uri) and, from Links.Volumes, the Volumes it belongs to (get_disk_inventory).
func ParseDrive(data []byte, uri string) (Props, error) {
	p, err := decodeProps(data)
	if err != nil {
		return nil, fmt.Errorf("%w: drive: %v", ErrMalformed, err)
	}
	return driveOf(p, uri), nil
}

func driveOf(p Props, uri string) Props {
	out := Props{"RedfishURI": uri}
	if s := str(p, "@odata.id"); servicePath(s) != "" {
		out["RedfishURI"] = s
	}
	for _, k := range driveProperties {
		v, ok := p[k]
		if !ok || v == nil {
			continue
		}
		if k == "Links" {
			if links, ok := v.(map[string]any); ok {
				if vols, ok := links["Volumes"].([]any); ok {
					ids := []any{}
					for _, x := range vols {
						if m, ok := x.(map[string]any); ok {
							if s, ok := m["@odata.id"].(string); ok {
								ids = append(ids, s)
							}
						}
					}
					out["Volumes"] = ids
				}
			}
			continue
		}
		out[k] = v
	}
	return out
}

// ParseVolume decodes a Volume: its inventory properties and, from
// Links.Drives, Linked_drives, the Ids of the drives it uses (get_volume_inventory).
func ParseVolume(data []byte) (Props, error) {
	p, err := decodeProps(data)
	if err != nil {
		return nil, fmt.Errorf("%w: volume: %v", ErrMalformed, err)
	}
	return volumeOf(p), nil
}

func volumeOf(p Props) Props {
	out := pickSet(p, volumeProperties)
	if links, ok := p["Links"].(map[string]any); ok {
		if drives, ok := links["Drives"].([]any); ok {
			ids := []any{}
			for _, x := range drives {
				if m, ok := x.(map[string]any); ok {
					if s, ok := m["@odata.id"].(string); ok {
						ids = append(ids, map[string]any{"Id": lastSegment(s)})
					}
				}
			}
			out["Linked_drives"] = ids
		}
	}
	return out
}

// controllerName is the name the oracle gives a storage subsystem's controller:
// the first member of Controllers (c), else StorageControllers[0] of the
// subsystem, else def.
func controllerName(storage, first Props, def string) string {
	named := func(p Props) string {
		if n := str(p, "Name"); n != "" {
			return n
		}
		id := "1"
		if s := str(p, "Id"); s != "" {
			id = s
		}
		return "Controller " + id
	}
	if first != nil {
		return named(first)
	}
	if sc, ok := storage["StorageControllers"].([]any); ok && len(sc) > 0 && storage["Controllers"] == nil {
		if m, ok := sc[0].(map[string]any); ok {
			return named(m)
		}
	}
	return def
}

// reads counts the resources a storage command has read.
type reads struct{ n int }

func (r *reads) get(ctx context.Context, c *Client, p string) (Props, error) {
	if r.n++; r.n > maxStorageReads {
		return nil, fmt.Errorf("%w: more than %d storage resources", ErrMalformed, maxStorageReads)
	}
	m, _, err := c.getProps(ctx, p)
	return m, err
}

// storageMembers lists the Storage subsystems of a system.
func (c *Client) storageMembers(ctx context.Context, rd *reads, systemURI string) ([]string, error) {
	sys, err := rd.get(ctx, c, systemURI)
	if err != nil {
		return nil, err
	}
	coll := linkOf(sys, "Storage")
	if coll == "" {
		return nil, fmt.Errorf("%w: Storage resource not found", ErrNotFound)
	}
	return c.members(ctx, coll)
}

// firstController reads the first member of the Controllers collection a
// Storage links (nil when it has none).
func (c *Client) firstController(ctx context.Context, rd *reads, storage Props) (Props, error) {
	coll := linkOf(storage, "Controllers")
	if coll == "" {
		return nil, nil
	}
	uris, err := c.membersMax(ctx, coll, 1, 1)
	if err != nil || len(uris) == 0 {
		return nil, err
	}
	return rd.get(ctx, c, uris[0])
}

// StorageControllerInventory lists the controllers of a system's Storage
// subsystems: the members of each Controllers collection, else the deprecated
// StorageControllers array (get_storage_controller_inventory). An error when
// the system has no Storage or the Storage collection is empty.
func (c *Client) StorageControllerInventory(ctx context.Context, systemURI string) ([]Props, error) {
	rd := &reads{}
	members, err := c.storageMembers(ctx, rd, systemURI)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("%w: Storage resource not found", ErrNotFound)
	}
	out := []Props{}
	for _, m := range members {
		st, err := rd.get(ctx, c, m)
		if err != nil {
			return nil, err
		}
		if coll := linkOf(st, "Controllers"); coll != "" {
			uris, err := c.members(ctx, coll)
			if err != nil {
				return nil, err
			}
			for _, u := range uris {
				cp, err := rd.get(ctx, c, u)
				if err != nil {
					return nil, err
				}
				out = append(out, pick(cp, controllerProperties...))
			}
		} else if list, ok := st["StorageControllers"].([]any); ok {
			for _, x := range list {
				if cp, ok := x.(map[string]any); ok {
					out = append(out, pick(cp, controllerProperties...))
				}
			}
		}
	}
	return out, nil
}

// DiskInventory lists the drives of a system by controller (get_disk_inventory):
// one entry per Storage subsystem with its Controller name, StorageId and Drives;
// when the system has no Storage but a SimpleStorage, one entry per SimpleStorage
// with the properties of its Devices. (The oracle's code tests the SimpleStorage
// link after the Storage branch has overwritten its variable, so a system that has
// both is read through Storage only: the same here.)
func (c *Client) DiskInventory(ctx context.Context, systemURI string) ([]Props, error) {
	rd := &reads{}
	sys, err := rd.get(ctx, c, systemURI)
	if err != nil {
		return nil, err
	}
	storage, simple := linkOf(sys, "Storage"), linkOf(sys, "SimpleStorage")
	if storage == "" && simple == "" {
		return nil, fmt.Errorf("%w: SimpleStorage and Storage resource not found", ErrNotFound)
	}
	out := []Props{}
	if storage != "" {
		members, err := c.members(ctx, storage)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			st, err := rd.get(ctx, c, m)
			if err != nil {
				return nil, err
			}
			first, err := c.firstController(ctx, rd, st)
			if err != nil {
				return nil, err
			}
			id := str(st, "Id")
			if id == "" {
				id = lastSegment(m)
			}
			drives := []any{}
			list, _ := st["Drives"].([]any)
			for _, x := range list {
				dm, _ := x.(map[string]any)
				du, _ := dm["@odata.id"].(string)
				if servicePath(du) == "" || len(drives) >= maxStorageReads {
					continue
				}
				dp, err := rd.get(ctx, c, servicePath(du))
				if err != nil {
					return nil, err
				}
				drives = append(drives, driveOf(dp, servicePath(du)))
			}
			out = append(out, Props{"Controller": controllerName(st, first, "Controller 1"), "StorageId": id, "Drives": drives})
		}
		return out, nil
	}
	members, err := c.members(ctx, simple)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		st, err := rd.get(ctx, c, m)
		if err != nil {
			return nil, err
		}
		name := str(st, "Name")
		if name == "" {
			id := "1"
			if s := str(st, "Id"); s != "" {
				id = s
			}
			name = "Controller " + id
		}
		drives := []any{}
		devs, _ := st["Devices"].([]any)
		for _, x := range devs {
			if d, ok := x.(map[string]any); ok && len(drives) < maxStorageReads {
				drives = append(drives, pick(d, driveProperties...))
			}
		}
		out = append(out, Props{"Controller": name, "Drives": drives})
	}
	return out, nil
}

// VolumeInventory lists the volumes of a system by controller
// (get_volume_inventory): one entry per Storage subsystem with its Controller
// name and its Volumes. A system without Storage is an error (a SimpleStorage
// has no volumes).
func (c *Client) VolumeInventory(ctx context.Context, systemURI string) ([]Props, error) {
	rd := &reads{}
	sys, err := rd.get(ctx, c, systemURI)
	if err != nil {
		return nil, err
	}
	storage, simple := linkOf(sys, "Storage"), linkOf(sys, "SimpleStorage")
	if storage == "" {
		if simple == "" {
			return nil, fmt.Errorf("%w: SimpleStorage and Storage resource not found", ErrNotFound)
		}
		return nil, fmt.Errorf("%w: Storage resource not found", ErrNotFound)
	}
	members, err := c.members(ctx, storage)
	if err != nil {
		return nil, err
	}
	out := []Props{}
	for i, m := range members {
		st, err := rd.get(ctx, c, m)
		if err != nil {
			return nil, err
		}
		first, err := c.firstController(ctx, rd, st)
		if err != nil {
			return nil, err
		}
		vols := []any{}
		if coll := linkOf(st, "Volumes"); coll != "" {
			uris, err := c.members(ctx, coll)
			if err != nil {
				return nil, err
			}
			for _, u := range uris {
				vp, err := rd.get(ctx, c, u)
				if err != nil {
					return nil, err
				}
				vols = append(vols, volumeOf(vp))
			}
		}
		out = append(out, Props{"Controller": controllerName(st, first, fmt.Sprintf("Controller %d", i)), "Volumes": vols})
	}
	return out, nil
}

// storageOf resolves a Storage subsystem of a system by the last segment of its
// path (the oracle's storage_subsystem_id); with no id, the only one.
func (c *Client) storageOf(ctx context.Context, rd *reads, systemURI, id string) (string, Props, error) {
	members, err := c.storageMembers(ctx, rd, systemURI)
	if err != nil {
		return "", nil, err
	}
	if len(members) == 0 {
		return "", nil, fmt.Errorf("%w: the Storage collection has no members", ErrNotFound)
	}
	uri, err := resolve(members, id, "Storage subsystem")
	if err != nil {
		return "", nil, err
	}
	st, err := rd.get(ctx, c, uri)
	return uri, st, err
}

// VolumeRequest is a volume to create (create_volume's volume_details).
type VolumeRequest struct {
	// Details are the properties of the new Volume: RAIDType is required; Name,
	// CapacityBytes and others are sent as given.
	Details Props
	// Drives are the paths of the drives it uses (Links.Drives): required.
	Drives []string
	// DeleteNoneVolumes first deletes the volumes of RAIDType None of the
	// subsystem (storage_none_volume_deletion). It destroys data.
	DeleteNoneVolumes bool
	// OnDelete, when set, is called with the paths about to be deleted by
	// DeleteNoneVolumes, before the first DELETE.
	OnDelete func(uris []string)
}

// BuildVolumePayload is the POST body of a volume: the details with Drives
// moved to Links.Drives as @odata.id objects (create_volume). RAIDType and at
// least one drive are required; a drive must be a path of the service.
func BuildVolumePayload(details Props, drives []string) (Props, error) {
	raid, _ := details["RAIDType"].(string)
	if raid == "" || len(drives) == 0 {
		return nil, errors.New("[RAIDType, Drives] are required parameter to create a volume")
	}
	if len(drives) > maxStorageReads {
		return nil, fmt.Errorf("more than %d drives", maxStorageReads)
	}
	pl := Props{}
	for k, v := range details {
		if k == "Drives" || k == "Links" {
			return nil, fmt.Errorf("the detail %s is set by the drives list", k)
		}
		pl[k] = v
	}
	list := make([]any, 0, len(drives))
	for _, d := range drives {
		if servicePath(d) == "" {
			return nil, fmt.Errorf("%w: the drive %q is not a path of the service (/redfish/...)", ErrMalformed, d)
		}
		list = append(list, map[string]any{"@odata.id": servicePath(d)})
	}
	pl["Links"] = map[string]any{"Drives": list}
	return pl, nil
}

// CheckVolumeRequest checks a volume against what its Storage subsystem says:
// the RAID type must be one of the controller's SupportedRAIDTypes (when it
// lists them) and each drive one of the subsystem's Drives (when it lists
// any). The service has the last word; this refuses what it would refuse, before
// anything is deleted. The drives are compared as paths.
func CheckVolumeRequest(raid string, drives, supported, own []string) error {
	if len(supported) > 0 && !slices.Contains(supported, raid) {
		return fmt.Errorf("%w: RAIDType %q is not one of the controller's SupportedRAIDTypes (%s)", ErrMismatch, raid, strings.Join(supported, ", "))
	}
	if len(own) > 0 {
		for _, d := range drives {
			if !slices.Contains(own, d) {
				return fmt.Errorf("%w: the drive %s does not belong to the Storage subsystem", ErrMismatch, d)
			}
		}
	}
	return nil
}

func linkPaths(p Props, key string) []string {
	var out []string
	list, _ := p[key].([]any)
	for _, x := range list {
		m, _ := x.(map[string]any)
		if s, _ := m["@odata.id"].(string); servicePath(s) != "" {
			out = append(out, servicePath(s))
		}
	}
	return out
}

// CreateVolume creates a volume in a Storage subsystem of a system (create_volume).
// The subsystem is named by the last segment of its path (the oracle's
// storage_subsystem_id; with none, the only one). The answer is 201 with the new
// volume, or 202 with a task monitor in Outcome.Monitor.
func (c *Client) CreateVolume(ctx context.Context, systemURI, storageID string, req VolumeRequest) (Outcome, error) {
	rd := &reads{}
	suri, st, err := c.storageOf(ctx, rd, systemURI, storageID)
	if err != nil {
		return Outcome{}, err
	}
	payload, err := BuildVolumePayload(req.Details, req.Drives)
	if err != nil {
		return Outcome{}, err
	}
	coll := linkOf(st, "Volumes")
	if coll == "" {
		return Outcome{}, fmt.Errorf("%w: %s has no Volumes collection", ErrUnsupported, suri)
	}
	var supported []string
	if first, err := c.firstController(ctx, rd, st); err == nil && first != nil {
		supported = stringList(first["SupportedRAIDTypes"])
	}
	if err := CheckVolumeRequest(str(req.Details, "RAIDType"), pathsOf(payload), supported, linkPaths(st, "Drives")); err != nil {
		return Outcome{}, err
	}
	var monitors []string
	if req.DeleteNoneVolumes {
		uris, err := c.members(ctx, coll)
		if err != nil {
			return Outcome{}, err
		}
		var none []string
		for _, u := range uris {
			vp, err := rd.get(ctx, c, u)
			if err != nil {
				return Outcome{}, err
			}
			if vp["RAIDType"] == "None" {
				none = append(none, u)
			}
		}
		if req.OnDelete != nil && len(none) > 0 {
			req.OnDelete(none)
		}
		for _, u := range none {
			r, err := c.Delete(ctx, u, "")
			if err != nil {
				return Outcome{}, err
			}
			if r.IsTask() {
				monitors = append(monitors, r.Location())
			}
		}
	}
	r, err := c.Post(ctx, coll, payload)
	if err != nil {
		return Outcome{}, err
	}
	if r.IsTask() {
		monitors = append(monitors, r.Location())
	}
	o := Outcome{Changed: true, Message: "Volume Created", Monitors: monitors}
	if len(monitors) > 0 {
		o.Monitor = monitors[len(monitors)-1]
	}
	return o, nil
}

func pathsOf(payload Props) []string {
	links, _ := payload["Links"].(map[string]any)
	var out []string
	list, _ := links["Drives"].([]any)
	for _, x := range list {
		m, _ := x.(map[string]any)
		out = append(out, str(m, "@odata.id"))
	}
	return out
}

// DeleteVolumesOptions tune DeleteVolumes.
type DeleteVolumesOptions struct {
	// DryRun resolves the volumes and deletes nothing.
	DryRun bool
	// OnPlan is called with the paths of the volumes to delete, before the first
	// DELETE (and in a dry run).
	OnPlan func(uris []string)
}

// DeleteVolumes deletes the volumes of a Storage subsystem named by their Ids
// (delete_volumes). The Ids are required and each must be a volume of the
// subsystem: unlike the oracle, which skips an Id it does not find and says the
// volumes "were deleted", nothing is deleted when one is unknown.
func (c *Client) DeleteVolumes(ctx context.Context, systemURI, storageID string, ids []string, o DeleteVolumesOptions) (Outcome, error) {
	if len(ids) == 0 {
		return Outcome{}, errors.New("redfish: name the volumes to delete (their Ids)")
	}
	rd := &reads{}
	suri, st, err := c.storageOf(ctx, rd, systemURI, storageID)
	if err != nil {
		return Outcome{}, err
	}
	coll := linkOf(st, "Volumes")
	if coll == "" {
		return Outcome{}, fmt.Errorf("%w: %s has no Volumes collection", ErrUnsupported, suri)
	}
	uris, err := c.members(ctx, coll)
	if err != nil {
		return Outcome{}, err
	}
	if len(uris) == 0 {
		return Outcome{Changed: false, Message: "VolumeCollection's Members array is either empty or missing"}, nil
	}
	var plan []string
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		u, err := resolve(uris, id, "volume")
		if err != nil {
			return Outcome{}, err
		}
		plan = append(plan, u)
	}
	if o.OnPlan != nil {
		o.OnPlan(plan)
	}
	if o.DryRun {
		return Outcome{Changed: false, Message: fmt.Sprintf("dry run: would delete %s", strings.Join(plan, ", "))}, nil
	}
	out := Outcome{Changed: true, Message: fmt.Sprintf("The following volumes were deleted: %s", strings.Join(ids, ", "))}
	for _, u := range plan {
		r, err := c.Delete(ctx, u, "")
		if err != nil {
			return out, fmt.Errorf("deleting %s: %w", u, err)
		}
		if r.IsTask() {
			out.Monitors = append(out.Monitors, r.Location())
		}
	}
	if len(out.Monitors) > 0 {
		out.Monitor = out.Monitors[0]
	}
	return out, nil
}
