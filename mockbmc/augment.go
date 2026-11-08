package mockbmc

import "strings"

// augment adds to the mockups what the commands of the Systems area need and
// the DMTF trees do not carry. Each addition is a resource or a property a real
// BMC has, written the way DSP8010 defines it; the mockup files are not edited.
//
//   - ComputerSystem.Boot.BootOrder and BootOptions (two BootOption resources),
//     PowerRestorePolicy, and the SetDefaultBootOrder action;
//   - on the rackmount tree, an ActionInfo for ComputerSystem.Reset that does not
//     allow "On" but allows "ForceOn", so that the client's mapping of a ResetType
//     to an allowed one is exercised (DSP0266 9.9.6);
//   - on a Bios resource, the Settings object takes ApplyTime Immediate (below,
//     in patch).
func (b *BMC) augment(tree string) {
	// The BIOS attribute registry the Bios resources name (AttributeRegistry 1.3):
	// a MessageRegistryFile that locates its document by language.
	const reg = root + "/Registries/BiosAttributeRegistryP89.v1_0_0"
	if b.res[root+"/Registries"] != nil {
		b.addMember(root+"/Registries", reg, map[string]any{"@odata.type": "#MessageRegistryFile.v1_1_0.MessageRegistryFile", "Id": "BiosAttributeRegistryP89.v1_0_0",
			"Name": "BIOS Attribute Registry File", "Languages": []any{"en"}, "Registry": "BiosAttributeRegistryP89.v1_0_0",
			"Location": []any{map[string]any{"Language": "en", "Uri": reg + "/en"}}})
		b.res[reg+"/en"] = map[string]any{"@odata.type": "#AttributeRegistry.v1_3_8.AttributeRegistry", "@odata.id": reg + "/en", "Id": "BiosAttributeRegistryP89.v1_0_0",
			"Language": "en", "RegistryEntries": map[string]any{"Attributes": []any{
				map[string]any{"AttributeName": "ProcTurboMode", "Type": "Enumeration", "ReadOnly": false, "Value": []any{map[string]any{"ValueName": "Enabled"}, map[string]any{"ValueName": "Disabled"}}},
				map[string]any{"AttributeName": "AdminPhone", "Type": "String", "ReadOnly": false}}}}
	}
	var systems []string
	for p := range b.res {
		if strings.HasPrefix(p, root+"/Systems/") && strings.Count(p, "/") == 4 {
			systems = append(systems, p)
		}
	}
	for _, p := range systems {
		m := b.res[p]
		boot, _ := m["Boot"].(map[string]any)
		if boot == nil {
			boot = map[string]any{}
			m["Boot"] = boot
		}
		if _, ok := boot["BootOrder"]; !ok {
			boot["BootOrder"] = []any{"Boot0001", "Boot0002"}
			boot["BootOptions"] = map[string]any{"@odata.id": p + "/BootOptions"}
			b.res[p+"/BootOptions"] = map[string]any{"@odata.type": "#BootOptionCollection.BootOptionCollection", "@odata.id": p + "/BootOptions",
				"Name": "Boot Option Collection", "Members": []any{}, "Members@odata.count": 0}
			b.addMember(p+"/BootOptions", p+"/BootOptions/Boot0001", map[string]any{"@odata.type": "#BootOption.v1_0_4.BootOption", "Id": "Boot0001", "Name": "UEFI Network", "DisplayName": "UEFI Network Boot", "BootOptionReference": "Boot0001"})
			b.addMember(p+"/BootOptions", p+"/BootOptions/Boot0002", map[string]any{"@odata.type": "#BootOption.v1_0_4.BootOption", "Id": "Boot0002", "Name": "UEFI Disk", "DisplayName": "UEFI Hard Disk", "BootOptionReference": "Boot0002"})
		}
		if _, ok := m["PowerRestorePolicy"]; !ok {
			m["PowerRestorePolicy"] = "LastState"
		}
		acts, _ := m["Actions"].(map[string]any)
		if acts == nil {
			acts = map[string]any{}
			m["Actions"] = acts
		}
		acts["#ComputerSystem.SetDefaultBootOrder"] = map[string]any{"target": p + "/Actions/ComputerSystem.SetDefaultBootOrder"}
		if reset, _ := acts["#ComputerSystem.Reset"].(map[string]any); reset != nil && tree == TreeRackmount {
			ai := p + "/ResetActionInfo"
			reset["@Redfish.ActionInfo"] = ai
			b.res[ai] = map[string]any{"@odata.type": "#ActionInfo.v1_3_0.ActionInfo", "@odata.id": ai, "Id": "ResetActionInfo", "Name": "Reset Action Info",
				"Parameters": []any{map[string]any{"Name": "ResetType", "Required": true, "DataType": "String",
					"AllowableValues": []any{"ForceOn", "ForceOff", "GracefulShutdown", "GracefulRestart", "ForceRestart", "PushPowerButton", "PowerCycle"}}}}
		}
	}
	b.augmentStorage()
	b.augmentManagers(tree)
	b.augmentUpdate(tree)
	b.augmentEvents(tree)
	b.augmentCertificates()
	b.augmentTelemetry()
}

// augmentManagers adds what the Managers commands need:
//   - Manager.ServiceIdentification, and a Manager.ResetToDefaults action (the
//     rackmount lists its ResetType values as an AllowableValues annotation, the
//     emulator as an ActionInfo that does not allow PreserveNetworkAndUsers, so
//     that both ways of telling the values, and a refusal, are exercised);
//   - an NTP service in ManagerNetworkProtocol (NTPServers, ProtocolEnabled);
//   - VirtualMedia: CD1 of every VirtualMedia collection gets the InsertMedia and
//     EjectMedia actions (with TransferProtocolType allowable values), while
//     Floppy1 has none and takes a PATCH of Image and Inserted, with a
//     TransferMethod whose allowable values the resource itself lists
//     (@Redfish.AllowableValues next to the property, DSP0266 9.9.2).
func (b *BMC) augmentManagers(tree string) {
	for p, m := range b.res {
		switch {
		case strings.HasPrefix(p, root+"/Managers/") && strings.Count(p, "/") == 4:
			if _, ok := m["ServiceIdentification"]; !ok {
				m["ServiceIdentification"] = "mock-bmc-" + tree
			}
			acts, _ := m["Actions"].(map[string]any)
			if acts == nil {
				acts = map[string]any{}
				m["Actions"] = acts
			}
			a := map[string]any{"target": p + "/Actions/Manager.ResetToDefaults"}
			if tree == TreeRackmount {
				a["ResetType@Redfish.AllowableValues"] = []any{"ResetAll", "PreserveNetworkAndUsers", "PreserveNetwork"}
			} else {
				ai := p + "/ResetToDefaultsActionInfo"
				a["@Redfish.ActionInfo"] = ai
				b.res[ai] = map[string]any{"@odata.type": "#ActionInfo.v1_3_0.ActionInfo", "@odata.id": ai, "Id": "ResetToDefaultsActionInfo", "Name": "ResetToDefaults Action Info",
					"Parameters": []any{map[string]any{"Name": "ResetType", "Required": true, "DataType": "String", "AllowableValues": []any{"ResetAll", "PreserveNetwork"}}}}
			}
			acts["#Manager.ResetToDefaults"] = a
		case strings.HasPrefix(p, root+"/Managers/") && strings.HasSuffix(p, "/NetworkProtocol"):
			if _, ok := m["NTP"]; !ok {
				m["NTP"] = map[string]any{"ProtocolEnabled": false, "NTPServers": []any{}}
			}
		case strings.Contains(p, "/VirtualMedia/") && strings.HasSuffix(p, "/CD1"):
			acts, _ := m["Actions"].(map[string]any)
			if acts == nil {
				acts = map[string]any{}
				m["Actions"] = acts
			}
			acts["#VirtualMedia.InsertMedia"] = map[string]any{"target": p + "/Actions/VirtualMedia.InsertMedia",
				"TransferProtocolType@Redfish.AllowableValues": []any{"HTTP", "HTTPS", "NFS"}}
			acts["#VirtualMedia.EjectMedia"] = map[string]any{"target": p + "/Actions/VirtualMedia.EjectMedia"}
			m["TransferProtocolType"] = "HTTPS"
		case strings.Contains(p, "/VirtualMedia/") && strings.HasSuffix(p, "/Floppy1"):
			m["TransferMethod"] = "Stream"
			m["TransferMethod@Redfish.AllowableValues"] = []any{"Stream", "Upload"}
		}
	}
}
