emulator-1u: the static resource tree of DMTF/Redfish-Interface-Emulator
(api_emulator/redfish/static, commit 5f51d8ac48e46665a2a85242434f125aa7b25172,
BSD-3-Clause, LICENSE-DMTF-Redfish-Interface-Emulator.md), copied unchanged,
except index.json at its root.

That file is ours: the emulator builds its service root in code
(api_emulator/static_resource_manager.py, property "configuration"); index.json
repeats the links of that property so the tree can be served from files.

Why a second tree: it differs from public-rackmount1 in what discovery must
cope with. The service root has no Links.Sessions; the Chassis is "1U" and the
Manager "BMC"; SessionService, a Systems tree of one system, Sensors and
PowerSubsystem under the Chassis, and some collections still use the old
"Links.Members" shape. mockbmc adds the SessionService/Sessions collection
when a tree has none, so that a session can be opened.
