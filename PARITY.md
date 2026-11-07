# Parity with Ansible community.general and DMTF redfishtool

go-redfish is meant to do everything the three Ansible modules `redfish_command`, `redfish_config` and `redfish_info` do, through one typed Go client and one CLI, tested against a mock BMC.

**Oracle.** `plugins/module_utils/_redfish_utils.py` and the three modules of ansible-collections/community.general at commit `33e521d68de99cf9eac0dc42194a24457a0b6aea` (2026-11-05; the helper is now called `_redfish_utils.py`): the `CATEGORY_COMMANDS_ALL` table of each module is the list of rows below, nothing added, nothing left out. **Cross-check.** The operations of DMTF redfishtool (its README, "Subcommand Operations"), second table.

**Reading it.** One row per command: command, the go-redfish function, the CLI command, the test that proves it, and a status. A row is **done**, **N/A** with a reason, or **todo** with the release that brings it (the releases are one minor per area: v0.4.0 Systems, v0.5.0 Chassis, v0.6.0 Managers, v0.7.0 Accounts, v0.8.0 Update, v0.9.0 Storage, Events, Certificates). When the last row is done, a row that is still **todo** is a defect of this file, and a test (`parity_test.go`) fails on one.

## Ansible commands

| Module | Category | Command | go-redfish | CLI | Test | Status |
|---|---|---|---|---|---|---|
| redfish_command | Systems | PowerOn | `Client.SystemPower` | `systems power PowerOn` | `TestPowerCommands`, `TestSystemsListAndPower` | done |
| redfish_command | Systems | PowerForceOff | `Client.SystemPower` | `systems power PowerForceOff` | `TestPowerCommands`, `TestSystemsListAndPower` | done |
| redfish_command | Systems | PowerForceRestart | `Client.SystemPower` | `systems power PowerForceRestart` | `TestPowerCommands`, `TestSystemsListAndPower` | done |
| redfish_command | Systems | PowerGracefulRestart | `Client.SystemPower` | `systems power PowerGracefulRestart` | `TestPowerCommands`, `TestSystemsListAndPower` | done |
| redfish_command | Systems | PowerGracefulShutdown | `Client.SystemPower` | `systems power PowerGracefulShutdown` | `TestPowerCommands`, `TestSystemsListAndPower` | done |
| redfish_command | Systems | PowerReboot | `Client.SystemPower` | `systems power PowerReboot` | `TestPowerCommands`, `TestSystemsListAndPower` | done |
| redfish_command | Systems | PowerCycle | `Client.SystemPower` | `systems power PowerCycle` | `TestPowerCommands`, `TestSystemsListAndPower` | done |
| redfish_command | Systems | PowerFullPowerCycle | `Client.SystemPower` | `systems power PowerFullPowerCycle` | `TestPowerCommands`, `TestSystemsListAndPower` | done |
| redfish_command | Systems | SetOneTimeBoot | `Client.SetBootOverride` | `systems boot-override set DEVICE` | `TestBootOverride`, `TestSystemsBoot` | done |
| redfish_command | Systems | EnableContinuousBootOverride | `Client.SetBootOverride` | `systems boot-override set DEVICE --continuous` | `TestBootOverride`, `TestSystemsBoot` | done |
| redfish_command | Systems | DisableBootOverride | `Client.SetBootOverride` | `systems boot-override disable` | `TestBootOverride`, `TestSystemsBoot` | done |
| redfish_command | Systems | IndicatorLedOn | `Client.SetIndicatorLED` | `systems led on` | `TestSecureBootPowerRestoreLedAndAssetTag`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_command | Systems | IndicatorLedOff | `Client.SetIndicatorLED` | `systems led off` | `TestSecureBootPowerRestoreLedAndAssetTag`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_command | Systems | IndicatorLedBlink | `Client.SetIndicatorLED` | `systems led blink` | `TestSecureBootPowerRestoreLedAndAssetTag`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_command | Systems | VirtualMediaInsert | `Client.VirtualMediaInsert` | `systems virtual-media insert` | `TestVirtualMedia`, `TestVirtualMediaQuirkyVendors`, `TestLogsAndVirtualMediaCommands` | done |
| redfish_command | Systems | VirtualMediaEject | `Client.VirtualMediaEject` | `systems virtual-media eject` | `TestVirtualMedia`, `TestLogsAndVirtualMediaCommands` | done |
| redfish_command | Systems | VerifyBiosAttributes | `Client.VerifyBiosAttributes` | `systems bios verify` | `TestBios`, `TestSystemsBios` | done |
| redfish_command | Chassis | IndicatorLedOn | `Client.SetIndicatorLED` | `chassis led on` | `TestChassisIndicatorLED`, `TestChassisLedCommands` | done |
| redfish_command | Chassis | IndicatorLedOff | `Client.SetIndicatorLED` | `chassis led off` | `TestChassisIndicatorLED`, `TestChassisLedCommands` | done |
| redfish_command | Chassis | IndicatorLedBlink | `Client.SetIndicatorLED` | `chassis led blink` | `TestChassisIndicatorLED`, `TestChassisLedCommands` | done |
| redfish_command | Accounts | AddUser | `Client.AddUser` | `accounts add` | `TestAccountLifecycle`, `TestAddUserWithoutAPlaceToPut`, `TestAccountsCommands`, `TestAccountPasswordSources`, `TestAnErrorNeverCarriesThePassword`, `FuzzAccountReads` | done |
| redfish_command | Accounts | EnableUser | `Client.EnableUser` | `accounts enable` | `TestAccountLifecycle`, `TestAccountsCommands` | done |
| redfish_command | Accounts | DeleteUser | `Client.DeleteUser` | `accounts delete` | `TestAccountLifecycle`, `TestAccountsCommands` | done |
| redfish_command | Accounts | DisableUser | `Client.DisableUser` | `accounts disable` | `TestAccountLifecycle`, `TestAccountsCommands` | done |
| redfish_command | Accounts | UpdateUserRole | `Client.UpdateUserRole` | `accounts role` | `TestAccountLifecycle`, `TestAccountsCommands` | done |
| redfish_command | Accounts | UpdateUserPassword | `Client.UpdateUserPassword`, `PasswordChangeURI` | `accounts password` | `TestAccountLifecycle`, `TestPasswordChangeRequired`, `TestAccountsCommands`, `TestAccountPasswordSources`, `TestAnErrorNeverCarriesThePassword`, `FuzzPasswordChangeURI`, `FuzzReadPassword` | done |
| redfish_command | Accounts | UpdateUserName | `Client.UpdateUserName` | `accounts rename` | `TestAccountLifecycle`, `TestAccountsCommands` | done |
| redfish_command | Accounts | UpdateUserAccountTypes | `Client.UpdateUserAccountTypes` | `accounts types` | `TestAccountLifecycle`, `TestAccountsCommands` | done |
| redfish_command | Accounts | UpdateAccountServiceProperties | `Client.UpdateAccountServiceProperties` | `accounts config set` | `TestAccountServiceConfig`, `TestAccountsCommands` | done |
| redfish_command | Sessions | ClearSessions | `Client.ClearSessions` | `session clear` | `TestSessions`, `TestSessionCommands` | done |
| redfish_command | Sessions | CreateSession | `Client.Login`, `Client.Session` | `session login` | `TestSessionLoginResumeAndLogout`, `TestSessionLoginUseLogout` | done |
| redfish_command | Sessions | DeleteSession | `Client.Close`, `Client.Delete` | `session logout`, `raw delete` | `TestSessionLoginResumeAndLogout`, `TestSessionLoginUseLogout` | done |
| redfish_command | Manager | GracefulRestart | `Client.ManagerPower` (maps it to PowerGracefulRestart), `Client.WaitAvailable` | `managers power GracefulRestart [--wait]` | `TestManagerPower`, `TestWaitAvailable`, `TestManagersReadsAndReset` | done |
| redfish_command | Manager | ClearLogs | `Client.ClearLogs` | `managers logs clear` | `TestLogsAndClearLogs`, `TestLogsAndVirtualMediaCommands` | done |
| redfish_command | Manager | VirtualMediaInsert | `Client.VirtualMediaInsert` | `managers virtual-media insert` | `TestVirtualMedia`, `TestVirtualMediaQuirkyVendors`, `TestVirtualMediaSlotRules`, `TestLogsAndVirtualMediaCommands` | done |
| redfish_command | Manager | ResetToDefaults | `Client.ManagerResetToDefaults` | `managers reset-to-defaults` | `TestManagerResetToDefaults`, `TestManagersReadsAndReset` | done |
| redfish_command | Manager | VirtualMediaEject | `Client.VirtualMediaEject` | `managers virtual-media eject` | `TestVirtualMedia`, `TestVirtualMediaSlotRules`, `TestLogsAndVirtualMediaCommands` | done |
| redfish_command | Manager | PowerOn | `Client.ManagerPower` | `managers power PowerOn` | `TestManagerPower`, `TestManagersReadsAndReset` | done |
| redfish_command | Manager | PowerForceOff | `Client.ManagerPower` | `managers power PowerForceOff` | `TestManagerPower`, `TestManagersReadsAndReset` | done |
| redfish_command | Manager | PowerForceRestart | `Client.ManagerPower` | `managers power PowerForceRestart` | `TestManagerPower`, `TestManagersReadsAndReset` | done |
| redfish_command | Manager | PowerGracefulRestart | `Client.ManagerPower` | `managers power PowerGracefulRestart` | `TestManagerPower`, `TestManagersReadsAndReset` | done |
| redfish_command | Manager | PowerGracefulShutdown | `Client.ManagerPower` | `managers power PowerGracefulShutdown` | `TestManagerPower`, `TestManagersReadsAndReset` | done |
| redfish_command | Manager | PowerReboot | `Client.ManagerPower` | `managers power PowerReboot` | `TestManagerPower`, `TestManagersReadsAndReset` | done |
| redfish_command | Update | SimpleUpdate | `Client.SimpleUpdate`, `CheckImageURI` | `update simple` | `TestSimpleUpdate`, `TestSimpleUpdateRefusals`, `TestUpdateCommands`, `FuzzCheckImageURI` | done |
| redfish_command | Update | MultipartHTTPPushUpdate | `Client.MultipartHTTPPushUpdate`, `MultipartFrame` | `update push` | `TestMultipartPush`, `TestMultipartPushStreams`, `TestMultipartPushRefusals`, `TestUpdatePushCommand`, `FuzzMultipartFrame` | done |
| redfish_command | Update | PerformRequestedOperations | `Client.PerformRequestedOperations` | `update operations` | `TestPerformRequestedOperations`, `TestUpdateOperationsCommand` | done |
| redfish_config | Systems | SetBiosDefaultSettings | `Client.ResetBios` | `systems bios reset` | `TestBios`, `TestSystemsBios` | done |
| redfish_config | Systems | SetBiosAttributes | `Client.SetBiosAttributes` | `systems bios set` | `TestBios`, `TestSystemsBios` | done |
| redfish_config | Systems | SetBootOrder | `Client.SetBootOrder` | `systems boot-order set` | `TestBootOrder`, `TestSystemsBoot` | done |
| redfish_config | Systems | SetDefaultBootOrder | `Client.SetDefaultBootOrder` | `systems boot-order default` | `TestBootOrder`, `TestSystemsBoot` | done |
| redfish_config | Systems | EnableSecureBoot | `Client.SetSecureBoot` | `systems secure-boot enable` | `TestSecureBootPowerRestoreLedAndAssetTag`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_config | Systems | SetSecureBoot | `Client.SetSecureBoot` | `systems secure-boot enable`, `systems secure-boot disable` | `TestSecureBootPowerRestoreLedAndAssetTag`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_config | Systems | DeleteVolumes | `Client.DeleteVolumes` | `storage volume delete` | `TestCreateAndDeleteVolume`, `TestVolumeTasks`, `TestStorageVolumeCreateDelete`, `TestStorageVolumeTasks` | done |
| redfish_config | Systems | CreateVolume | `Client.CreateVolume`, `BuildVolumePayload`, `CheckVolumeRequest` | `storage volume create` | `TestCreateAndDeleteVolume`, `TestVolumeTasks`, `TestBuildVolumePayload`, `TestStorageVolumeCreateDelete`, `FuzzVolumeRequest` | done |
| redfish_config | Systems | SetPowerRestorePolicy | `Client.SetPowerRestorePolicy` | `systems power-restore-policy set` | `TestSecureBootPowerRestoreLedAndAssetTag`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_config | Manager | SetNetworkProtocols | `Client.SetNetworkProtocols`, `NetworkProtocolPayload`, `Client.SetNTP` | `managers network-protocols set`, `managers ntp set` | `TestNetworkProtocolsAndNTP`, `TestNetworkProtocolPayload`, `FuzzNetworkProtocolPayload`, `TestManagersConfig` | done |
| redfish_config | Manager | SetManagerNic | `Client.SetManagerNIC` | `managers nic set` | `TestManagerNICs`, `TestManagersConfig` | done |
| redfish_config | Manager | SetHostInterface | `Client.SetHostInterface` | `managers host-interface set` | `TestHostInterfaces`, `TestManagersConfig` | done |
| redfish_config | Manager | SetServiceIdentification | `Client.SetServiceIdentification` | `managers service-id set` | `TestManagerDateTimeAndServiceIdentification`, `TestManagersConfig` | done |
| redfish_config | Sessions | SetSessionService | `Client.SetSessionService` | `session config` | `TestSessions`, `TestSessionCommands` | done |
| redfish_info | Systems | GetSystemInventory | `Client.SystemInventory` | `systems inventory system` | `TestInventories`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_info | Systems | GetPsuInventory | `Client.PSUInventory` | `systems inventory psu` | `TestInventories`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_info | Systems | GetCpuInventory | `Client.CPUInventory` | `systems inventory cpu` | `TestInventories`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_info | Systems | GetMemoryInventory | `Client.MemoryInventory` | `systems inventory memory` | `TestInventories`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_info | Systems | GetNicInventory | `Client.NICInventory` | `systems inventory nic` | `TestInventories`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_info | Systems | GetHealthReport | `Client.SystemHealthReport`, `Client.HealthReport` | `systems health` | `TestInventories`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_info | Systems | GetStorageControllerInventory | `Client.StorageControllerInventory`, `ParseController` | `storage controllers` | `TestStorageInventories`, `TestSimpleStorageOnly`, `TestStorageCommands`, `FuzzStorageReads` | done |
| redfish_info | Systems | GetDiskInventory | `Client.DiskInventory`, `ParseDrive` | `storage disks` | `TestStorageInventories`, `TestSimpleStorageOnly`, `TestStorageCommands`, `FuzzStorageReads` | done |
| redfish_info | Systems | GetVolumeInventory | `Client.VolumeInventory`, `ParseVolume` | `storage volumes` | `TestStorageInventories`, `TestSimpleStorageOnly`, `TestStorageCommands`, `FuzzStorageReads` | done |
| redfish_info | Systems | GetBiosAttributes | `Client.BiosAttributes` | `systems bios get` | `TestBios`, `TestSystemsBios` | done |
| redfish_info | Systems | GetBootOrder | `Client.BootOrder` | `systems boot-order get` | `TestBootOrder`, `TestSystemsBoot` | done |
| redfish_info | Systems | GetBootOverride | `Client.BootOverrideOf` | `systems boot-override get` | `TestBootOverride`, `TestSystemsBoot` | done |
| redfish_info | Systems | GetVirtualMedia | `Client.VirtualMedia` | `systems virtual-media get` | `TestVirtualMedia`, `TestLogsAndVirtualMediaCommands` | done |
| redfish_info | Systems | GetBiosRegistries | `Client.BiosRegistry` | `systems bios registry` | `TestBios`, `TestSystemsBios` | done |
| redfish_info | Systems | GetPowerRestorePolicy | `Client.PowerRestorePolicy` | `systems power-restore-policy get` | `TestSecureBootPowerRestoreLedAndAssetTag`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_info | Chassis | GetFanInventory | `Client.FanInventory` | `chassis fans` | `TestChassisOracleReads`, `TestChassisCommands` | done |
| redfish_info | Chassis | GetPsuInventory | `Client.PSUInventory` | `chassis psu` | `TestChassisOracleReads`, `TestChassisCommands` | done |
| redfish_info | Chassis | GetChassisPower | `Client.ChassisPower` | `chassis power` | `TestChassisOracleReads`, `TestChassisCommands` | done |
| redfish_info | Chassis | GetChassisThermals | `Client.ChassisThermals` | `chassis thermals` | `TestChassisOracleReads`, `TestChassisCommands`, `TestChassisLegacyAbsenceAndHPE` | done |
| redfish_info | Chassis | GetChassisInventory | `Client.ChassisInventory` | `chassis inventory` | `TestChassisOracleReads`, `TestChassisCommands` | done |
| redfish_info | Chassis | GetHealthReport | `Client.ChassisHealthReports`, `Client.ChassisHealthReport` | `chassis health` | `TestChassisOracleReads`, `TestChassisCommands` | done |
| redfish_info | Chassis | GetHPEThermalConfig | `Client.HPEThermalConfig` | `chassis hpe thermal-config` | `TestChassisLegacyAbsenceAndHPE`, `TestChassisCommands` | done |
| redfish_info | Chassis | GetHPEFanPercentMin | `Client.HPEFanPercentMin` | `chassis hpe fan-percent-min` | `TestChassisLegacyAbsenceAndHPE`, `TestChassisCommands` | done |
| redfish_info | Accounts | ListUsers | `Client.ListUsers` | `accounts list` | `TestAccountLifecycle`, `TestAccountsCommands`, `FuzzAccountReads` | done |
| redfish_info | Accounts | GetAccountServiceConfig | `Client.AccountServiceConfig` | `accounts config` | `TestAccountServiceConfig`, `TestAccountsCommands` | done |
| redfish_info | Sessions | GetSessions | `Client.GetSessions` | `session list` | `TestSessions`, `TestSessionCommands` | done |
| redfish_info | Update | GetFirmwareInventory | `Client.FirmwareInventory` | `update inventory` | `TestUpdateInventories`, `TestUpdateCommands`, `FuzzUpdateReads` | done |
| redfish_info | Update | GetFirmwareUpdateCapabilities | `Client.FirmwareUpdateCapabilities`, `ParseUpdateCapabilities` | `update capabilities` | `TestFirmwareUpdateCapabilities`, `TestUpdateCommands`, `FuzzUpdateReads` | done |
| redfish_info | Update | GetSoftwareInventory | `Client.SoftwareInventory` | `update inventory --software` | `TestUpdateInventories`, `TestUpdateCommands`, `FuzzUpdateReads` | done |
| redfish_info | Update | GetUpdateStatus | `Client.UpdateStatus`, `Client.WaitUpdate`, `ParseUpdateStatus` | `update status` | `TestUpdateStatusAndHandles`, `TestParseUpdateStatus`, `TestUpdateCommands`, `FuzzOperationResults` | done |
| redfish_info | Manager | GetManagerNicInventory | `Client.ManagerNICInventory`, `Client.EachManager` | `managers nic get` | `TestManagerNICs`, `TestManagersReadsAndReset` | done |
| redfish_info | Manager | GetVirtualMedia | `Client.VirtualMedia` | `managers virtual-media get` | `TestVirtualMedia`, `TestLogsAndVirtualMediaCommands` | done |
| redfish_info | Manager | GetLogs | `Client.Logs`, `ParseLogEntries` | `managers logs` | `TestLogsAndClearLogs`, `TestParseLogEntries`, `FuzzParseLogEntries`, `TestManagersReadsAndReset` | done |
| redfish_info | Manager | GetNetworkProtocols | `Client.NetworkProtocols` | `managers network-protocols get` | `TestNetworkProtocolsAndNTP`, `TestManagersReadsAndReset` | done |
| redfish_info | Manager | GetHealthReport | `Client.ManagerHealthReport`, `Client.EachManager` | `managers health` | `TestManagerResolveInventoryHealth`, `TestManagersReadsAndReset` | done |
| redfish_info | Manager | GetHostInterfaces | `Client.HostInterfaces` | `managers host-interface get` | `TestHostInterfaces`, `TestManagersConfig` | done |
| redfish_info | Manager | GetManagerInventory | `Client.ManagerInventory`, `Client.EachManager` | `managers inventory` | `TestManagerResolveInventoryHealth`, `TestManagersReadsAndReset`, `FuzzManagerReads` | done |
| redfish_info | Manager | GetServiceIdentification | `Client.ServiceIdentification`, `Client.ResolveServiceIdentification` | `managers service-id get` | `TestManagerDateTimeAndServiceIdentification`, `TestManagersConfig` | done |
| redfish_info | Service | CheckAvailability | `Client.Root`, `Client.Get("/redfish")` | `raw get /redfish`, `raw get /redfish/v1/` | `TestRawPutHeadVersionsOdataMetadata`, `TestRawGetJSONAndTable` | done |

## redfishtool operations (cross-check)

| redfishtool | Maps to | Status |
|---|---|---|
| `versions`, `root`, `odata`, `metadata` | `raw get /redfish`, `/redfish/v1/`, `/redfish/v1/odata`, `/redfish/v1/$metadata` (`TestRawPutHeadVersionsOdataMetadata`) | done |
| `raw GET|PATCH|POST|DELETE|HEAD|PUT` | `raw get|patch|post|delete|head|put` (`Client.Do`; `TestRawPatchPostDelete`, `TestRawPutHeadVersionsOdataMetadata`) | done |
| `SessionService login`, `logout` | the Sessions rows above | done |
| `SessionService setSessionTimeout`, `Sessions list`, `patch` | `session config --set SessionTimeout=N`, `session list`; `session clear` is the ClearSessions row (`TestSessions`, `TestSessionCommands`) | done |
| `Systems list|get|patch`, `Processors`, `Inventory`, `EthernetInterfaces`, `SimpleStorage` | `systems list`, `systems inventory` (`system`, `cpu`, `memory`, `nic`, `psu`), `raw get`, `raw patch` (`TestSystemsListAndPower`, `TestInventories`); `SimpleStorage` is read by `storage disks` (the Devices of each SimpleStorage when the system has no Storage; `TestSimpleStorageOnly`), as GetDiskInventory does | done |
| `Systems reset`, `setAssetTag`, `setIndicatorLed`, `setBootOverride` | `systems power`, `systems led`, `systems boot-override set`; `setAssetTag` has no Ansible row: added as `Client.SetAssetTag`, `systems asset-tag` (`TestPowerCommands`, `TestSecureBootPowerRestoreLedAndAssetTag`) | done |
| `Systems Logs`, `clearLog`; Chassis and Managers likewise | `GetLogs`, `ClearLogs` for a manager (`managers logs`, `managers logs clear`); the same functions read and clear the LogServices of a system and of a chassis (`systems logs`, `chassis logs`; `TestLogsAndClearLogs`, `TestLogsAndVirtualMediaCommands`) | done |
| `Chassis Power`, `Thermal`, `Sensors`, `getPowerReading`, `setPowerLimit` | `chassis power`, `chassis thermals` (`TestChassisOracleReads`); Sensors, PowerSubsystem, ThermalSubsystem and EnvironmentMetrics are added: `chassis sensors`, `power-subsystem`, `thermal-subsystem`, `metrics` (`TestChassisNewResources`; DSP8010 Chassis 1.29, the schemas the Meter already reads); `getPowerReading` is `chassis metrics` (PowerWatts) and `chassis power` (PowerConsumedWatts) | done |
| `Chassis setPowerLimit` | N/A: it has no Ansible row, and the limit lives in an array of the deprecated Power resource (PowerControl[n].PowerLimit) or in a Controls resource, which services handle differently; `raw patch` sets it | N/A |
| `Managers reset`, `NetworkProtocol`, `EthernetInterfaces` | `managers power`, `managers network-protocols get` and `set`, `managers nic get` and `set` (the `GracefulRestart`, `SetNetworkProtocols`, `SetManagerNic`, `GetNetworkProtocols` and `GetManagerNicInventory` rows) | done |
| `Managers setDateTime`, `setTimeOffset` | no Ansible row: added as `Client.SetManagerDateTime`, `Client.ManagerDateTime`, `managers datetime get` and `set` (and `Client.SetNTP`, `managers ntp set`, a flat view of the NTP service of SetNetworkProtocols) (`TestManagerDateTimeAndServiceIdentification`, `TestNetworkProtocolsAndNTP`, `TestManagersConfig`) | done |
| `Managers SerialInterfaces` | N/A: no Ansible row and no write; `raw get /redfish/v1/Managers/ID/SerialInterfaces` reads them | N/A |
| `AccountService adduser`, `deleteuser`, `setpassword`, `useradmin`, `setusername`, `Accounts`, `Roles` | `accounts add`, `delete`, `password`, `role`, `rename`, `list`, `config`, and `accounts roles` (`Client.Roles`: no Ansible row, a read of the Roles collection; `TestAccountLifecycle`, `TestAccountsCommands`); `useradmin` is `accounts enable`, `disable` and `role` | done |
| `hello`, `about`, `examples` (developer commands) | N/A: they print redfishtool's own help | N/A |
| `-W/--Wait`, `-C/--CheckRedfishVersion`, `-R/--RedfishVersion` | `Client.Root` reports RedfishVersion; the CLI does not wait for or select a protocol version: Redfish has one major version, v1, in the URL (DSP0266 7.2.3) | N/A |
| `-A None|Basic|Session`, `-S Secure` | basic and session are `--password-file` and `session login`; "None" and non-https are refused on purpose (DSP0266 13.3.3: basic needs HTTPS; a password is never sent in clear) | N/A |
| `-N/--NonBlocking` | a task monitor is returned, not awaited, unless `--watch` is given: `update simple`, `update push` and `update status` (`Client.WaitTask`, `Client.WaitUpdate`; `TestUpdateCommands`, `TestUpdatePushCommand`) | done |
| `-I/-M/-F/-1/-L` (select a member) | each area command takes `--id` or picks the only member; several members and no `--id` is `ErrAmbiguous` (`systems --id`, `chassis --id`, `managers --id`; `TestResolveAmbiguous`, `TestManagerResolveInventoryHealth`) | done (`storage --id` names the system; `TestStorageCommands`) |

## Beyond the Ansible modules

The eventing of Redfish (v0.10.0) has no Ansible oracle: the three modules have no command for it. The reference is the specification itself, as published and opened on 2026-11-07: DSP0266 1.23.0 (2025-09-05, PDF) 12.1 Eventing, 12.5 Server-sent events and 13.7 Event subscriptions; the DSP8010 2025.4 schemas EventService v1_12_0, EventDestination v1_17_1 and Event v1_14_0 (redfish.dmtf.org/schemas/v1); and, for the wire format of the stream that DSP0266 12.5.1 points to, the WHATWG HTML Living Standard 9.2 (server-sent events). A row is one capability, with the section that defines it. `parity_test.go` (`TestParityBeyondTheAnsibleModules`) checks that every row is done and names tests that exist.

| Spec document | Section and capability | go-redfish | CLI | Test | Status |
|---|---|---|---|---|---|
| DSP0266 1.23.0, EventService 1.12.0 | 12.1.5: read the EventService (ServiceEnabled, ServerSentEventUri, EventFormatTypes, RegistryPrefixes, ResourceTypes, SSEFilterPropertiesSupported); ErrUnsupported without one | `Client.EventService` | `events status` | `TestEventServiceRead`, `TestEventsStatusSubscriptionsAndTest` | done |
| DSP0266 1.23.0, EventDestination 1.17.1 | 12.1.2: list and read the subscriptions (the EventDestination collection; the SSE ones included, 12.5.2); HttpHeaders never read back | `Client.Subscriptions`, `Client.Subscription` | `events subscriptions [ID]` | `TestSubscriptionLifecycle`, `TestEventsStatusSubscriptionsAndTest` | done |
| DSP0266 1.23.0, EventDestination 1.17.1 | 12.1.2, 12.1.5 Table 37: create a subscription (POST, 201 and Location; Destination, Protocol, EventFormatType, RegistryPrefixes, ResourceTypes, MessageIds, Exclude*, OriginResources, SubordinateResources, Context) checked against the EventService | `Client.Subscribe`, `SubscribeOptions` | `events subscribe` | `TestSubscriptionLifecycle`, `TestSubscribeRefusals`, `TestEventsSubscribeAndUnsubscribe` | done |
| DSP0266 1.23.0, EventDestination 1.17.1 | 13.1, 13.7: a Destination is https, without credentials, query or fragment; HttpHeaders (credentials) from a 0600 file or stdin, redacted from errors | `CheckDestination`, `SubscribeOptions.HTTPHeaders` | `events subscribe --headers-file`, `--headers-stdin` | `TestSubscribeRefusals`, `TestSubscribeErrorNeverCarriesTheHeader`, `FuzzCheckDestination`, `FuzzReadHeaders` | done |
| DSP0266 1.23.0 | 12.1.2: delete a subscription (DELETE of the member; a 404 is not a change) | `Client.Unsubscribe` | `events unsubscribe` | `TestSubscriptionLifecycle`, `TestEventsSubscribeAndUnsubscribe` | done |
| EventService 1.12.0 | SubmitTestEvent action, its parameters checked against the ActionInfo or AllowableValues (DSP0266 9.9.2, 9.9.6); ErrUnsupported when the service has only the 1.0 SendTestEvent | `Client.SubmitTestEvent`, `TestEvent` | `events test` | `TestSubmitTestEvent`, `TestEventsStatusSubscriptionsAndTest` | done |
| DSP0266 1.23.0 | 12.5.1, 12.5.2: open the SSE stream (200 text/event-stream), authenticated like any request, cancelled by the context; the service's SSE EventDestination appears and goes away | `Client.Events`, `Client.EventsWith`, `EventStream` | `events listen` | `TestEventsStreamAndCancel`, `TestEventsEndedByTheService`, `TestEventsOpenRefusals`, `TestEventsListen`, `TestEventsListenInterrupted` | done |
| DSP0266 1.23.0 | 12.5.2: `$filter` of the SSE URI (EventFormatType, MessageId, RegistryPrefix, OriginResource, ResourceType, SubordinateResources, `and`, `or`, parentheses), refused client-side for a property SSEFilterPropertiesSupported denies | `EventsOptions.Filter` | `events listen --filter` | `TestEventsFilter`, `TestEventsFilterRefusals` | done |
| DSP0266 1.23.0, WHATWG HTML 9.2 | 12.5.2: the id field, Last-Event-ID reconnection (after a broken connection only, bounded; not after a clean end), the retry field | `EventsOptions.MaxReconnects`, `SSEReader.LastID` | `events listen --reconnects` | `TestEventsResumeWithLastEventID`, `TestEventsNoReconnectByDefaultOfEventsWith`, `TestEventsEndedByTheService` | done |
| WHATWG HTML 9.2.5, 9.2.6; DSP0266 1.23.0 12.1.2 | The stream parser, bounded: lines, events and records over the limit (1 MiB, DSP0266 12.1.2) are refused, never buffered | `SSEReader`, `ErrEventTooLarge` | `events listen --max-line`, `--max-event-bytes` | `TestSSEReaderRules`, `TestSSEReaderChunking`, `TestSSEReaderBounds`, `TestEventsOversizedEventIsRefused`, `TestEventsListenOversizedEvent`, `FuzzSSEReader` | done |
| DSP0266 1.23.0, Event 1.14.0 | 12.5.2.1, 12.5.2.2: the Event payload (and a MetricReport) decoded | `ParseEvent`, `Event`, `EventRecord` | `events listen` (`-o json`, `-o table`) | `TestSpecEventExample`, `TestParseEvent`, `FuzzParseEvent` | done |

**Not implemented**, and why: push delivery (the service POSTs to a receiver: that is the receiver's side, which a client library does not run); subscriptions of other protocols (SNMP, SMTP, Syslog, Kafka, OEM) and SubscriptionType other than RedfishEvent (they need receivers and settings this client has no use for); the deprecated EventTypes (DSP0266 12.1.4); `Severities`, `MetricReportDefinitions`, `IncludeOriginOfCondition`, `DeliveryRetryPolicy`, `SendHeartbeat` and `VerifyCertificate` of a subscription, and the SSE `includeoriginofcondition` query parameter (read in the EventService, not yet settable); the `$filter` criteria EventType and MetricReportDefinition of the stream (the mock does not support them, and the client refuses them when a service says so); the actions Suspend/ResumeSubscription and TestEventSubscription; updating the EventService (ServiceEnabled, retry settings) other than with `raw patch`.
