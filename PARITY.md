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
| redfish_command | Systems | VirtualMediaInsert |  |  |  | todo (v0.6.0 Managers) |
| redfish_command | Systems | VirtualMediaEject |  |  |  | todo (v0.6.0 Managers) |
| redfish_command | Systems | VerifyBiosAttributes | `Client.VerifyBiosAttributes` | `systems bios verify` | `TestBios`, `TestSystemsBios` | done |
| redfish_command | Chassis | IndicatorLedOn | `Client.SetIndicatorLED` | `chassis led on` | `TestSecureBootPowerRestoreLedAndAssetTag`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_command | Chassis | IndicatorLedOff | `Client.SetIndicatorLED` | `chassis led off` | `TestSecureBootPowerRestoreLedAndAssetTag`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_command | Chassis | IndicatorLedBlink | `Client.SetIndicatorLED` | `chassis led blink` | `TestSecureBootPowerRestoreLedAndAssetTag`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_command | Accounts | AddUser |  |  |  | todo (v0.7.0 Accounts) |
| redfish_command | Accounts | EnableUser |  |  |  | todo (v0.7.0 Accounts) |
| redfish_command | Accounts | DeleteUser |  |  |  | todo (v0.7.0 Accounts) |
| redfish_command | Accounts | DisableUser |  |  |  | todo (v0.7.0 Accounts) |
| redfish_command | Accounts | UpdateUserRole |  |  |  | todo (v0.7.0 Accounts) |
| redfish_command | Accounts | UpdateUserPassword |  |  |  | todo (v0.7.0 Accounts) |
| redfish_command | Accounts | UpdateUserName |  |  |  | todo (v0.7.0 Accounts) |
| redfish_command | Accounts | UpdateUserAccountTypes |  |  |  | todo (v0.7.0 Accounts) |
| redfish_command | Accounts | UpdateAccountServiceProperties |  |  |  | todo (v0.7.0 Accounts) |
| redfish_command | Sessions | ClearSessions |  |  |  | todo (v0.3.0 Sessions) |
| redfish_command | Sessions | CreateSession | `Client.Login`, `Client.Session` | `session login` | `TestSessionLoginResumeAndLogout`, `TestSessionLoginUseLogout` | done |
| redfish_command | Sessions | DeleteSession | `Client.Close`, `Client.Delete` | `session logout`, `raw delete` | `TestSessionLoginResumeAndLogout`, `TestSessionLoginUseLogout` | done |
| redfish_command | Manager | GracefulRestart |  |  |  | todo (v0.6.0 Managers) |
| redfish_command | Manager | ClearLogs |  |  |  | todo (v0.6.0 Managers) |
| redfish_command | Manager | VirtualMediaInsert |  |  |  | todo (v0.6.0 Managers) |
| redfish_command | Manager | ResetToDefaults |  |  |  | todo (v0.6.0 Managers) |
| redfish_command | Manager | VirtualMediaEject |  |  |  | todo (v0.6.0 Managers) |
| redfish_command | Manager | PowerOn |  |  |  | todo (v0.6.0 Managers) |
| redfish_command | Manager | PowerForceOff |  |  |  | todo (v0.6.0 Managers) |
| redfish_command | Manager | PowerForceRestart |  |  |  | todo (v0.6.0 Managers) |
| redfish_command | Manager | PowerGracefulRestart |  |  |  | todo (v0.6.0 Managers) |
| redfish_command | Manager | PowerGracefulShutdown |  |  |  | todo (v0.6.0 Managers) |
| redfish_command | Manager | PowerReboot |  |  |  | todo (v0.6.0 Managers) |
| redfish_command | Update | SimpleUpdate |  |  |  | todo (v0.8.0 Update) |
| redfish_command | Update | MultipartHTTPPushUpdate |  |  |  | todo (v0.8.0 Update) |
| redfish_command | Update | PerformRequestedOperations |  |  |  | todo (v0.8.0 Update) |
| redfish_config | Systems | SetBiosDefaultSettings | `Client.ResetBios` | `systems bios reset` | `TestBios`, `TestSystemsBios` | done |
| redfish_config | Systems | SetBiosAttributes | `Client.SetBiosAttributes` | `systems bios set` | `TestBios`, `TestSystemsBios` | done |
| redfish_config | Systems | SetBootOrder | `Client.SetBootOrder` | `systems boot-order set` | `TestBootOrder`, `TestSystemsBoot` | done |
| redfish_config | Systems | SetDefaultBootOrder | `Client.SetDefaultBootOrder` | `systems boot-order default` | `TestBootOrder`, `TestSystemsBoot` | done |
| redfish_config | Systems | EnableSecureBoot | `Client.SetSecureBoot` | `systems secure-boot enable` | `TestSecureBootPowerRestoreLedAndAssetTag`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_config | Systems | SetSecureBoot | `Client.SetSecureBoot` | `systems secure-boot enable`, `systems secure-boot disable` | `TestSecureBootPowerRestoreLedAndAssetTag`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_config | Systems | DeleteVolumes |  |  |  | todo (v0.9.0 Storage) |
| redfish_config | Systems | CreateVolume |  |  |  | todo (v0.9.0 Storage) |
| redfish_config | Systems | SetPowerRestorePolicy | `Client.SetPowerRestorePolicy` | `systems power-restore-policy set` | `TestSecureBootPowerRestoreLedAndAssetTag`, `TestSystemsSecureBootPolicyLedAssetTagInventoryHealth` | done |
| redfish_config | Manager | SetNetworkProtocols |  |  |  | todo (v0.6.0 Managers) |
| redfish_config | Manager | SetManagerNic |  |  |  | todo (v0.6.0 Managers) |
| redfish_config | Manager | SetHostInterface |  |  |  | todo (v0.6.0 Managers) |
| redfish_config | Manager | SetServiceIdentification |  |  |  | todo (v0.6.0 Managers) |
| redfish_config | Sessions | SetSessionService |  |  |  | todo (v0.3.0 Sessions) |
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
| redfish_info | Systems | GetVirtualMedia |  |  |  | todo (v0.6.0 Managers) |
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
| redfish_info | Accounts | ListUsers |  |  |  | todo (v0.7.0 Accounts) |
| redfish_info | Accounts | GetAccountServiceConfig |  |  |  | todo (v0.7.0 Accounts) |
| redfish_info | Sessions | GetSessions |  |  |  | todo (v0.3.0 Sessions) |
| redfish_info | Update | GetFirmwareInventory |  |  |  | todo (v0.8.0 Update) |
| redfish_info | Update | GetFirmwareUpdateCapabilities |  |  |  | todo (v0.8.0 Update) |
| redfish_info | Update | GetSoftwareInventory |  |  |  | todo (v0.8.0 Update) |
| redfish_info | Update | GetUpdateStatus |  |  |  | todo (v0.8.0 Update) |
| redfish_info | Manager | GetManagerNicInventory |  |  |  | todo (v0.6.0 Managers) |
| redfish_info | Manager | GetVirtualMedia |  |  |  | todo (v0.6.0 Managers) |
| redfish_info | Manager | GetLogs |  |  |  | todo (v0.6.0 Managers) |
| redfish_info | Manager | GetNetworkProtocols |  |  |  | todo (v0.6.0 Managers) |
| redfish_info | Manager | GetHealthReport |  |  |  | todo (v0.6.0 Managers) |
| redfish_info | Manager | GetHostInterfaces |  |  |  | todo (v0.6.0 Managers) |
| redfish_info | Manager | GetManagerInventory |  |  |  | todo (v0.6.0 Managers) |
| redfish_info | Manager | GetServiceIdentification |  |  |  | todo (v0.6.0 Managers) |
| redfish_info | Service | CheckAvailability | `Client.Root`, `Client.Get("/redfish")` | `raw get /redfish`, `raw get /redfish/v1/` | `TestRawPutHeadVersionsOdataMetadata`, `TestRawGetJSONAndTable` | done |

## redfishtool operations (cross-check)

| redfishtool | Maps to | Status |
|---|---|---|
| `versions`, `root`, `odata`, `metadata` | `raw get /redfish`, `/redfish/v1/`, `/redfish/v1/odata`, `/redfish/v1/$metadata` (`TestRawPutHeadVersionsOdataMetadata`) | done |
| `raw GET|PATCH|POST|DELETE|HEAD|PUT` | `raw get|patch|post|delete|head|put` (`Client.Do`; `TestRawPatchPostDelete`, `TestRawPutHeadVersionsOdataMetadata`) | done |
| `SessionService login`, `logout` | the Sessions rows above | done |
| `SessionService setSessionTimeout`, `Sessions list`, `patch` | `SetSessionService`, `GetSessions` | todo (v0.3.0 Sessions) |
| `Systems list|get|patch`, `Processors`, `Inventory`, `EthernetInterfaces`, `SimpleStorage` | `systems list`, `systems inventory` (`system`, `cpu`, `memory`, `nic`, `psu`), `raw get`, `raw patch` (`TestSystemsListAndPower`, `TestInventories`); `SimpleStorage` is the GetStorageControllerInventory row | done, except `SimpleStorage` (todo v0.9.0 Storage) |
| `Systems reset`, `setAssetTag`, `setIndicatorLed`, `setBootOverride` | `systems power`, `systems led`, `systems boot-override set`; `setAssetTag` has no Ansible row: added as `Client.SetAssetTag`, `systems asset-tag` (`TestPowerCommands`, `TestSecureBootPowerRestoreLedAndAssetTag`) | done |
| `Systems Logs`, `clearLog`; Chassis and Managers likewise | `GetLogs`, `ClearLogs` | todo (v0.6.0 Managers) |
| `Chassis Power`, `Thermal`, `Sensors`, `getPowerReading`, `setPowerLimit` | `chassis power`, `chassis thermals` (`TestChassisOracleReads`); Sensors, PowerSubsystem, ThermalSubsystem and EnvironmentMetrics are added: `chassis sensors`, `power-subsystem`, `thermal-subsystem`, `metrics` (`TestChassisNewResources`; DSP8010 Chassis 1.29, the schemas the Meter already reads); `getPowerReading` is `chassis metrics` (PowerWatts) and `chassis power` (PowerConsumedWatts) | done |
| `Chassis setPowerLimit` | N/A: it has no Ansible row, and the limit lives in an array of the deprecated Power resource (PowerControl[n].PowerLimit) or in a Controls resource, which services handle differently; `raw patch` sets it | N/A |
| `Managers reset`, `setDateTime`, `setTimeOffset`, `NetworkProtocol`, `EthernetInterfaces`, `SerialInterfaces` | `GracefulRestart`, `SetNetworkProtocols`, `SetManagerNic`, `GetNetworkProtocols`, `GetManagerNicInventory`; date and time added | todo (v0.6.0 Managers) |
| `AccountService adduser|deleteuser|setpassword|useradmin|setusername|Accounts|Roles` | the Accounts rows | todo (v0.7.0 Accounts) |
| `hello`, `about`, `examples` (developer commands) | N/A: they print redfishtool's own help | N/A |
| `-W/--Wait`, `-C/--CheckRedfishVersion`, `-R/--RedfishVersion` | `Client.Root` reports RedfishVersion; the CLI does not wait for or select a protocol version: Redfish has one major version, v1, in the URL (DSP0266 7.2.3) | N/A |
| `-A None|Basic|Session`, `-S Secure` | basic and session are `--password-file` and `session login`; "None" and non-https are refused on purpose (DSP0266 13.3.3: basic needs HTTPS; a password is never sent in clear) | N/A |
| `-N/--NonBlocking` | a task monitor is returned, not awaited, unless `--watch` is given (`Client.WaitTask`) | todo (v0.8.0 Update) |
| `-I/-M/-F/-1/-L` (select a member) | each area command takes `--id` or picks the only member; several members and no `--id` is `ErrAmbiguous` (`systems --id`, `chassis --id`; `TestResolveAmbiguous`) | done for Systems and Chassis (the other areas follow with their release) |
