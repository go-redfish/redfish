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
| redfish_config | Systems | DeleteVolumes |  |  |  | todo (v0.9.0 Storage) |
| redfish_config | Systems | CreateVolume |  |  |  | todo (v0.9.0 Storage) |
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
| redfish_info | Systems | GetStorageControllerInventory |  |  |  | todo (v0.9.0 Storage) |
| redfish_info | Systems | GetDiskInventory |  |  |  | todo (v0.9.0 Storage) |
| redfish_info | Systems | GetVolumeInventory |  |  |  | todo (v0.9.0 Storage) |
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
| `Systems list|get|patch`, `Processors`, `Inventory`, `EthernetInterfaces`, `SimpleStorage` | `systems list`, `systems inventory` (`system`, `cpu`, `memory`, `nic`, `psu`), `raw get`, `raw patch` (`TestSystemsListAndPower`, `TestInventories`); `SimpleStorage` is the GetStorageControllerInventory row | done, except `SimpleStorage` (todo v0.9.0 Storage) |
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
| `-I/-M/-F/-1/-L` (select a member) | each area command takes `--id` or picks the only member; several members and no `--id` is `ErrAmbiguous` (`systems --id`, `chassis --id`, `managers --id`; `TestResolveAmbiguous`, `TestManagerResolveInventoryHealth`) | done for Systems, Chassis and Managers (the other areas follow with their release) |
