# redfish

[![Go Reference](https://pkg.go.dev/badge/github.com/go-redfish/redfish.svg)](https://pkg.go.dev/github.com/go-redfish/redfish)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue.svg)](LICENSE)
[![CI](https://github.com/go-redfish/redfish/actions/workflows/ci.yml/badge.svg)](https://github.com/go-redfish/redfish/actions/workflows/ci.yml)

**Part of [go-redfish](https://github.com/go-redfish)**: Go libraries for the Redfish API of a server's BMC. Documentation: <https://go-redfish.github.io/docs/>.

A small, self-contained Go client for the [DMTF Redfish](https://www.dmtf.org/standards/redfish)
API of a server's BMC, to read **what a node consumes**: the energy counter of its chassis
(`EnvironmentMetrics.EnergykWh`), else its power. Standard library only (the CLI adds cobra).

```go
c, err := redfish.New(redfish.Config{
    BaseURL:     "https://bmc-01.example.org",
    Credentials: redfish.Static{User: "monitor", Password: pw}, // or your own Credentials
    CAFile:      "/etc/site-ca.pem",
})
if err != nil { ... }
r, err := c.Read(ctx)
if err == nil && r.EnergyKWh != nil {
    fmt.Printf("%s: %.3f kWh since the BMC counted\n", r.Chassis, *r.EnergyKWh)
}
```

`Read` discovers the service once (service root, Chassis collection, each chassis' `EnvironmentMetrics`
and `Power` links) and remembers the links. With `Config.MetricsPath` it reads that resource and nothing
else. `Config.Session` logs in once and sends `X-Auth-Token` (`Close` logs out); otherwise HTTP Basic.

## A Redfish client, and a CLI (v0.3.0)

Beyond reading a node's consumption, go-redfish is becoming a **complete Redfish client** with a CLI to try it:
the goal is parity with the Ansible modules `redfish_command`, `redfish_config` and `redfish_info`
(community.general) and with DMTF redfishtool, tracked row by row in [PARITY.md](PARITY.md).

v0.3.0 brings the generic layer and the tools to test it:

- **`Client.Do`, `Get`, `Patch`, `Post`, `Delete`**: any operation on any path of the service, with `If-Match` on
  the entity tag a read returned (412 when it changed), a 202 answer's task monitor (`Response.Location`,
  `RetryAfter`, `Client.WaitTask`), and every error decoded: a non-2xx answer is a `*StatusError` carrying the
  error object's code and message and its `@Message.ExtendedInfo` messages (`ParseError`, fuzzed). Paths are
  `/redfish/...` only; a header cannot carry another credential; bodies are bounded.
- **Sessions**: `Login`, `Session()` (the token and the Session resource) and `Config.Token`/`SessionURI` to
  resume one; `Close` logs out.
- **`mockbmc`**: the DMTF *public-rackmount1* mockup (DSP2043, BSD-3-Clause, vendored with its licence) behind an
  `http.Handler` that is stateful enough to be driven like a BMC: authentication (Basic or token), sessions, ETags
  and 412, PATCH, creating and deleting members, `ComputerSystem.Reset` checked against its AllowableValues, a
  `SimpleUpdate` task with its monitor, and Redfish errors with ExtendedInfo.
- **`cmd/redfish`**, a cobra CLI (`go install github.com/go-redfish/redfish/cmd/redfish@latest`):

```sh
redfish mock serve --ca-out /tmp/mock-ca.pem          # prints endpoint, user admin / password "password"
echo -n password > ~/.mock-pw && chmod 600 ~/.mock-pw
redfish --endpoint https://127.0.0.1:PORT --user admin --password-file ~/.mock-pw --ca-file /tmp/mock-ca.pem \
        raw get /redfish/v1/Systems -o table
redfish ... session login      # the token goes to a 0600 file under the config directory, never to the screen
redfish --endpoint ... --ca-file ... --session raw get /redfish/v1/Chassis
redfish ... session logout     # DELETE at the BMC, then the file is removed
redfish ... raw patch /redfish/v1/Systems/1 --data '{"AssetTag":"rack-7"}' --if-match 'W/"..."'
```

  Flags: `--endpoint --user --password-file --ca-file --insecure --session --timeout -o json|table`. **There is no
  password flag and no environment variable for a secret**; the password file must be mode 0600. TLS is verified by
  default; `--insecure` is explicit and warns on stderr. `raw get|patch|put|post|delete|head` cover redfishtool's
  `raw`, and `root`, `versions`, `odata`, `metadata` are `raw get` of their paths.

## Systems (v0.4.0)

The Systems commands of the three Ansible modules, ported from community.general's `redfish_utils` (the oracle,
commit 33e521d6) and checked against both mock trees:

- **Library** (`systems.go`): `ResolveSystem`/`ResolveChassis` (the one resource, or the one named by id, else
  `ErrAmbiguous`); `SystemPower`/`ManagePower` (the `ResetType` the service allows, from its `ActionInfo` or
  `AllowableValues`, nothing sent when the state is already the one asked); `SetBootOverride`, `BootOverrideOf`,
  `BootOrder`, `SetBootOrder`, `SetDefaultBootOrder`; `BiosAttributes`, `SetBiosAttributes` (through the
  `@Redfish.Settings` object, with an optional `ApplyTime`), `VerifyBiosAttributes`, `ResetBios`, `BiosRegistry`;
  `SetSecureBoot`; `PowerRestorePolicy`/`SetPowerRestorePolicy`; `SetIndicatorLED`, `SetAssetTag`;
  `SystemInventory`, `CPUInventory`, `MemoryInventory`, `NICInventory`, `PSUInventory`, `SystemHealthReport`.
  A change is made with `PatchChecked`, which compares the payload with the resource first (`CheckPayload`):
  an unchanged state is reported as such (`Outcome.Changed` false), a property the resource lacks is
  `ErrUnsupported`. `ParseActionInfo`, `CheckPayload` and `Pick` are fuzzed.
- **CLI**: `redfish systems [--id ID] ...` and `redfish chassis [--id ID] led ...`:

```sh
redfish ... systems list
redfish ... systems power PowerForceOff                 # PowerOn, PowerReboot, PowerCycle, ...
redfish ... systems boot-override set Pxe --once        # or --continuous; boot-override get, disable
redfish ... systems boot-order get                      # boot-order set Boot0002 Boot0001, boot-order default
redfish ... systems bios set --attr ProcTurboMode=Disabled --apply-time OnReset   # bios get, verify, reset, registry
redfish ... systems secure-boot enable                  # or disable
redfish ... systems power-restore-policy set AlwaysOn   # or get
redfish ... systems led blink                           # on, off, blink; chassis led works the same
redfish ... systems asset-tag rack-7
redfish ... systems inventory cpu                       # system, cpu, memory, nic, psu
redfish ... systems health
```

  With several systems and no `--id`, the command fails and lists them (`ErrAmbiguous`).
- **Second mock tree**: `mockbmc.Options{Tree: mockbmc.TreeEmulator}` (`redfish mock serve --tree emulator-1u`) serves
  the DMTF Redfish-Interface-Emulator 1U mockup beside the default *public-rackmount1*, so that no command is
  tuned to one vendor tree; every Systems test runs on both.

## Chassis (v0.5.0)

The chassis half of `redfish_info` (GetChassisInventory, GetFanInventory, GetChassisPower, GetChassisThermals,
GetPsuInventory, GetHealthReport, GetHPEThermalConfig, GetHPEFanPercentMin), ported from community.general's
`redfish_utils` and run against both mock trees, plus the resources that replace the deprecated Power and Thermal:

- **Library** (`chassis.go`): `ChassisInventory`, `FanInventory`, `ChassisPower`, `ChassisThermals`,
  `ChassisHealthReport(s)`, `HPEThermalConfig`, `HPEFanPercentMin` (a chassis list, or every chassis when none is
  given, as the oracle does); `ChassisSensors` (the Sensor resources), `ChassisPowerSubsystem` (capacity,
  allocation, the power supplies present), `ChassisThermalSubsystem` (fans and ThermalMetrics) and
  `ChassisEnvironmentMetrics`. `SetIndicatorLED` serves chassis as well as systems. The reads of
  `Meter` and `Read` are unchanged.
- **CLI**: `redfish chassis [--id ID] ...`:

```sh
redfish ... chassis list
redfish ... chassis inventory                 # fans, power, thermals, psu, health, hpe thermal-config, hpe fan-percent-min
redfish ... chassis sensors                   # power-subsystem, thermal-subsystem, metrics
redfish ... chassis led blink                 # on, off, blink
redfish ... -o table chassis thermals         # a list is a table: one row per element
```

  The reading commands cover every chassis without `--id`; `led`, `sensors`, `power-subsystem`, `thermal-subsystem` and
  `metrics` act on one chassis and fail with `ErrAmbiguous` when there are several and none is named.
  `-o table` renders a list of objects as a table (it printed a Go slice in v0.4.0).

## Managers (v0.6.0)

Every Manager command of `redfish_command`, `redfish_config` and `redfish_info`, and the VirtualMedia commands
that the Systems category shares (VirtualMediaInsert, VirtualMediaEject, GetVirtualMedia), ported from
community.general's `redfish_utils` and run against both mock trees (the rackmount tree keeps its virtual media
on the system, the emulator tree on the manager; a mock `ClearLog`, `InsertMedia`, `EjectMedia` and
`ResetToDefaults` act on the resources):

- **Library** (`managers.go`): `ManagerURIs`, `ResolveManager`, `EachManager`, `ManagerInventory`,
  `ManagerHealthReport`, `ManagerNICInventory`; `ManagerPower` (GracefulRestart, PowerOn... with the ResetType
  the service allows) and `WaitAvailable`; `ManagerResetToDefaults`; `NetworkProtocols`, `SetNetworkProtocols`
  (`NetworkProtocolPayload` checks the services, states and ports, as the module does) and `SetNTP`;
  `ManagerDateTime`, `SetManagerDateTime`; `ServiceIdentification`, `SetServiceIdentification`;
  `SetManagerNIC`; `HostInterfaces`, `SetHostInterface`; `Logs` and `ClearLogs` (a manager, a system or a chassis);
  `VirtualMedia`, `VirtualMediaInsert` and `VirtualMediaEject` (a system or a manager: the action when the slot has
  it, else a PATCH; the HPE and Supermicro workarounds of the oracle). `ParseLogEntries`, `NetworkProtocolPayload`
  and the manager reads are fuzzed.
- **CLI**: `redfish managers [--id ID] ...`; virtual media and logs wherever the service puts them:

```sh
redfish ... managers list
redfish ... managers inventory                  # health, nic get, service-id get, datetime get
redfish ... managers power GracefulRestart --wait   # PowerOn, PowerForceOff, PowerReboot...; --wait blocks until the BMC answers
redfish ... managers reset-to-defaults PreserveNetwork
redfish ... managers network-protocols set --set SSH.ProtocolEnabled=off --set SSH.Port=2222
redfish ... managers ntp set --server 0.pool.ntp.org --enable
redfish ... managers datetime set --time 2026-11-05T17:00:00+01:00 --offset +01:00
redfish ... managers nic set --address 192.0.2.9 --json '{"IPv4StaticAddresses":{"Address":"192.0.2.9"}}'
redfish ... managers host-interface set --interface 1 --set InterfaceEnabled=false
redfish ... managers service-id set rack-7
redfish ... managers logs --max 20              # logs clear; also systems logs and chassis logs
redfish ... managers virtual-media insert --image https://host/os.iso --media-type CD   # get, eject; also systems virtual-media
```

  `inventory`, `health` and `nic get` cover every manager without `--id`; the others act on one manager and fail with
  `ErrAmbiguous` when there are several and none is named. A password for an image is read from
  `--image-password-file` (mode 0600), never given on the command line. `chassis led` is covered by its own tests
  (`TestChassisIndicatorLED`, `TestChassisLedCommands`).

## Accounts and sessions (v0.7.0)

The Accounts commands of `redfish_command` (AddUser, DeleteUser, EnableUser, DisableUser, UpdateUserRole,
UpdateUserPassword, UpdateUserName, UpdateUserAccountTypes, UpdateAccountServiceProperties), `ListUsers` and
`GetAccountServiceConfig` of `redfish_info`, and the Sessions commands (ClearSessions, SetSessionService,
GetSessions), ported from community.general's `redfish_utils`:

- **Library** (`accounts.go`): `ListUsers`, `AccountServiceConfig`, `Roles`, `AddUser`, `DeleteUser`, `EnableUser`,
  `DisableUser`, `UpdateUserRole`, `UpdateUserPassword`, `UpdateUserName`, `UpdateUserAccountTypes`,
  `UpdateAccountServiceProperties`, `GetSessions`, `ClearSessions`, `SetSessionService`, and `PasswordChangeURI`
  (the account a `Base.1.x.PasswordChangeRequired` refusal names). A service with a fixed number of accounts is
  handled as the oracle does: no POST in the `Allow` header (or a 405) is an empty slot changed by a PATCH, no
  DELETE is a PATCH that empties the account. A password is a `Secret`: it prints as `[redacted]` with every fmt
  verb, as JSON and in `log/slog`, and an error from a service that repeats it is redacted (text and fields).
- **CLI**: `redfish accounts list|roles|add|delete|enable|disable|password|role|rename|types|config [set]` and
  `redfish session list|clear|config`:

```sh
redfish ... accounts list
redfish ... accounts add bob --role Operator --new-password-file ~/.bob-pw     # or --new-password-stdin
printf '%s\n' "$NEWPW" | redfish ... accounts password bob --new-password-stdin
redfish ... accounts --id 3 rename carol        # an account is a USERNAME, or --id (its Id)
redfish ... accounts config set --set MinPasswordLength=12
redfish ... session list ; redfish ... session clear ; redfish ... session config --set SessionTimeout=600
```

  **The password of an account comes from a file of mode 0600 or from the standard input, never from a flag or the
  environment**, and is never printed. `session clear` ends the session it runs with last.
- **Mock**: account create, PATCH (rename, enable, disable, role, password) and delete under the AccountService
  rules (unique names, a role of the service, `MinPasswordLength`, a disabled account cannot log in,
  `PasswordChangeRequired` answers 403 with the `Base` message), the `Allow` header, and `Options.FixedSlots` /
  `OmitAllow` for the services with a fixed number of accounts.
- **Departures from the oracle**: the `Password` of an account is write-only (it reads back null or is absent), so it
  is never compared with the resource (the oracle's property check would refuse a service that omits it) and a
  password given is always sent; the Accounts collection is also found under `Links.Accounts` (the DMTF emulator's
  older AccountService); an account member that cannot be read is skipped as in the oracle, but an authentication
  failure is not; the Sessions collection falls back to the default path when the root has no `Links.Sessions`;
  `ClearSessions` reads the members, not `Members@odata.count`, and ends its own session last.

## UpdateService (v0.8.0)

The Update commands of `redfish_command` (SimpleUpdate, MultipartHTTPPushUpdate, PerformRequestedOperations) and
`redfish_info` (GetFirmwareInventory, GetSoftwareInventory, GetFirmwareUpdateCapabilities, GetUpdateStatus), and
redfishtool's `-N/--NonBlocking`:

- **Library** (`update.go`): `SimpleUpdate`, `MultipartHTTPPushUpdate`, `UpdateStatus`, `WaitUpdate`,
  `PerformRequestedOperations`, `FirmwareInventory`, `SoftwareInventory`, `FirmwareUpdateCapabilities`, and the pure
  decoders `ParseUpdateStatus`, `ParseUpdateCapabilities`, `CheckImageURI`, `MultipartFrame`. An update that starts a
  task returns its monitor (`Outcome.Monitor`, `UpdateStatus.Handle`); `WaitUpdate` follows it.
- **CLI**: `redfish update simple|push|status|operations|inventory [--software]|capabilities`. `simple`, `push` and
  `status` take `--watch` (with `--poll`, `--watch-timeout`) to follow the task monitor, with the progress on stderr;
  without it the monitor is returned and not awaited.

```sh
redfish ... update capabilities ; redfish ... update inventory ; redfish ... update inventory --software
redfish ... update simple --image https://images.example/bios.bin --protocol HTTPS --image-password-file ~/.img-pw --watch
redfish ... update push --file bmc-fw.bin --apply-time OnReset --max-size 1073741824 --watch
redfish ... update status /redfish/v1/TaskService/Tasks/1 --watch ; redfish ... update operations /redfish/v1/TaskService/Tasks/1
```

- **Security**: `push` streams the image from the file (a known Content-Length, never read whole), accepts only a
  regular file of at most `--max-size` (default 4 GiB; a larger file is `ErrImageTooLarge` before anything is sent) and
  bounds the upload by `--upload-timeout`; the names of extra parts are validated against header injection. The
  password of the image server is a `Secret` read from a 0600 file or the standard input, never a flag or the
  environment, redacted from errors and from the messages of the result; an `ImageURI` that holds a password, or whose
  scheme is not one of the service's `TransferProtocol` values, is refused, and the protocol, targets and apply time
  are checked against the service's allowable values. The decoders are fuzzed (`FuzzOperationResults`,
  `FuzzUpdateReads`, `FuzzCheckImageURI`, `FuzzMultipartFrame`).
- **Mock**: `MultipartHttpPushUri` takes a bounded multipart upload (`Options.MaxUpload`, 413 above), a task, a
  firmware and a software inventory, a `Targets` list for SimpleUpdate, and `Options.ResetRequired` (a
  `Base.ResetRequired` message in the task). The emulator tree has no UpdateService.
- **Departures from the oracle**: a 202 whose body is not a Task is `New` and keeps its handle (the oracle calls it
  Completed and drops it); a `ResetRequired` reset is posted to the resource's Reset action when it has one (the
  oracle posts to the resource itself, which services answer with 405) and only for a resource of the service; a
  `ResetRequired` or job message without its arguments is ignored (the oracle raises); the top-level
  `@Message.ExtendedInfo` of a success is read as well as the error object's; the oracle's `"ret": "False"` strings of
  GetFirmwareUpdateCapabilities are errors; an ImageURI is validated and a custom part gets `text/plain` when no
  media type is given (the oracle raises); the oracle's "NSF" in the default protocol list is kept.

## Storage (v0.9.0)

The last five rows of [PARITY.md](PARITY.md): `GetStorageControllerInventory`, `GetDiskInventory` and
`GetVolumeInventory` of `redfish_info`, `CreateVolume` and `DeleteVolumes` of `redfish_config`, and redfishtool's
`SimpleStorage`. **Parity: 99/99** rows done (no N/A, no todo; `parity_test.go` now fails on a todo row).

- **Library** (`storage.go`): `StorageControllerInventory`, `DiskInventory`, `VolumeInventory`, `CreateVolume`,
  `DeleteVolumes`, and the pure `ParseController`, `ParseDrive`, `ParseVolume`, `BuildVolumePayload`,
  `CheckVolumeRequest`. A 202 answer is returned as task monitors (`VolumeResult`: an `Outcome` and `Monitors`, one per request
  that answered 202).
- **CLI**: `redfish storage controllers|disks|volumes` and `redfish storage volume create|delete`, with `--id` for
  the system. `disks` reads Storage, else the Devices of SimpleStorage.

```sh
redfish ... storage controllers ; redfish ... storage disks ; redfish ... storage volumes
redfish ... storage volume create --storage RAID1 --raid-type RAID1 --drive /redfish/v1/Systems/1/Storage/RAID1/Drives/Disk1 --drive /redfish/v1/Systems/1/Storage/RAID1/Drives/Disk2
redfish ... storage volume delete --storage RAID1 --dry-run Vol1 ; redfish ... storage volume delete --storage RAID1 Vol1
```

- **Safety**: `volume delete` destroys data. Every volume is named by its Id: there is no delete-all (the oracle has
  none either); the paths to delete are printed on stderr before the first DELETE, `--dry-run` deletes nothing, and
  when one name is not a volume of the subsystem nothing is deleted. `--delete-none-volumes` of `create` (the oracle's
  `storage_none_volume_deletion`) is never implied and prints what it deletes first. Decoders and validators are
  fuzzed (`FuzzStorageReads`, `FuzzVolumeRequest`); reads are bounded (1024 resources a command); a drive must be a
  path of the service.
- **Mock**: neither mockup has a Storage resource, so both trees get `Storage/RAID1` (a Controllers collection with
  SupportedRAIDTypes RAID0/1/5, four drives, volumes Vol1 and Vol0 of RAIDType None) and `Storage/AHCI` (the
  deprecated StorageControllers array, one drive). A POST to a Volumes collection checks the RAID type, that the
  drives belong to the subsystem and are free, and their number; 201, or 202 with `Options.AsyncVolumes`.
  `Options.SimpleStorageOnly` leaves Storage out.
- **Departures from the oracle**: the RAID type and the drives are checked against SupportedRAIDTypes and the
  subsystem's Drives before the POST (the oracle leaves it to the service); `DeleteVolumes` with no Id is an error and
  with an unknown Id deletes nothing (the oracle skips it and reports the volumes "deleted"); an empty
  `storage_subsystem_id` is the only subsystem rather than an error; a drive or volume that cannot be read is an error
  (the oracle raises); a missing Name or Id of a controller gets the oracle's "Controller N" name instead of a
  KeyError; a system with both Storage and SimpleStorage is read through Storage only, which is what the oracle's code
  does in effect (it tests the SimpleStorage link on a variable the Storage branch has overwritten).

## Events (v0.10.0)

Beyond the Ansible modules, which have no eventing: the EventService, its subscriptions, the test event and the
Server-Sent Events stream. There is no Ansible oracle; the reference is the specification (DSP0266 1.23.0 12.1,
12.5 and 13.7; the DSP8010 schemas EventService 1.12.0, EventDestination 1.17.1 and Event 1.14.0; WHATWG HTML 9.2 for the
stream format). Each capability is a row of the section "Beyond the Ansible modules" of [PARITY.md](PARITY.md).

- **Library** (`events.go`, `events_stream.go`, `sse.go`): `EventService`, `Subscriptions`, `Subscription`,
  `Subscribe`, `Unsubscribe`, `SubmitTestEvent`, `CheckDestination`; `Events(ctx, filter)` and `EventsWith` return an
  `EventStream` (a channel `C` of `Event`, and `Err()` once it is closed); `SSEReader` and `ParseEvent` are the pure
  parser and decoder. A service without the feature gives `ErrUnsupported`.
- **CLI**: `redfish events status|subscriptions|subscribe|unsubscribe|test|listen`.

```sh
redfish ... events status
redfish ... events subscribe --destination https://receiver.example/events --registry-prefix Base --headers-file ~/.receiver-headers
redfish ... events test --message-id Contoso.1.0.TempAssert --message "hot"
redfish ... events listen --filter "RegistryPrefix eq 'Contoso'" -o json     # Ctrl-C ends it
```

- **Safety**: a subscription Destination must be https, without credentials, query or fragment. The HTTP headers the
  service sends to the receiver (an Authorization) are credentials: they come from a 0600 file or the standard input,
  never a flag, and are redacted from errors. The stream is authenticated like any request, over https; a line or an
  event above 1 MiB (or `--max-line`, `--max-event-bytes`) ends it with `ErrEventTooLarge`, nothing grows. It reconnects
  with `Last-Event-ID` only after a broken connection and a bounded number of times, never after a clean end by the
  service (which closes the connection when its subscription is deleted). The parser, the event decoder, the
  destination check and the headers file are fuzzed (`FuzzSSEReader`, `FuzzParseEvent`, `FuzzCheckDestination`,
  `FuzzReadHeaders`).
- **Mock**: the Subscriptions collection (POST 201 with Location, DELETE), `SubmitTestEvent`, and an SSE endpoint
  (`ServerSentEventUri`) with the `$filter` subset documented in `mockbmc/events.go`, ids, history and `Last-Event-ID`.
  The emulator tree has the old EventService 1.0 (no SSE, no SubmitTestEvent). The mock does not push to destinations.
- **Not implemented**: push receivers, non-Redfish protocols, and the other subscription properties (see PARITY.md).

## Certificates (v0.11.0)

Beyond the Ansible modules, which do not manage certificates: the CertificateService and the certificate
collections it manages. There is no Ansible oracle; the reference is the specification (DSP0266 1.23.0 9.9.13, 13.1.3
and 13.2; the DSP8010 schemas CertificateService 1.2.2, Certificate 1.11.0, CertificateLocations 1.0.4 and
CertificateCollection). Each capability is a row of the section "Beyond the Ansible modules" of
[PARITY.md](PARITY.md).

- **Library** (`certs.go`): `CertificateService`, `CertificateLocations`, `Certificate`, `Certificates` (the
  PEM string is decoded with `crypto/x509`: subject, issuer, validity, key, usages, SANs, fingerprint: `ParseX509`),
  `GenerateCSR` (the answer is decoded as PKCS#10: `ParseCSR`, `ParseCSRResponse`), `InstallCertificate` (POST to a
  collection), `ReplaceCertificate`, `DeleteCertificate`, `RekeyCertificate` and `RenewCertificate`. The actions are
  checked against the ActionInfo or the AllowableValues of the service and the formats of the collection against
  `@Redfish.SupportedCertificates`. A service without the feature gives `ErrUnsupported`.
- **CLI**: `redfish certs status|list|show|csr|replace|install|delete|rekey|renew`.

```sh
redfish ... certs list
redfish ... certs show /redfish/v1/Managers/BMC/NetworkProtocol/HTTPS/Certificates/1      # + the parsed x509 fields
redfish ... certs csr --collection /redfish/v1/Managers/BMC/NetworkProtocol/HTTPS/Certificates \
        --common-name bmc1.example.org --alt-name bmc1 --country FR --out bmc1.csr
redfish ... certs replace /redfish/v1/Managers/BMC/NetworkProtocol/HTTPS/Certificates/1 --cert-file bmc1.pem
redfish ... certs replace ID --cert-file other.pem --key-file other.key       # a certificate the service has no key for
```

- **Safety**: a private key the schema lets the client send (as part of the certificate string, for a certificate that
  was not made from a CSR of the service) comes only from a 0600 file (`--key-file`) or the standard input
  (`--key-stdin`), is a `Secret`, is checked against the certificate before anything is sent (one block, not
  encrypted, the certificate's public key), is never printed and is redacted from errors; a CSR or certificate answer
  that carries a key is refused or stripped. Strings are bounded (certificate 64 KiB, key 16 KiB, a chain of 16, 256
  certificates). The decoders and the key reader are fuzzed (`FuzzParseX509`, `FuzzParseCSRResponse`,
  `FuzzParseCertificateLocations`, `FuzzParseCertificateResource`, `FuzzReadKey`).
- **Mock**: a real self-signed certificate in the HTTPS collection, `CertificateLocations` kept up to date, POST to a
  certificate collection (decoded with `crypto/x509`, key checked, 400 when the service holds no key), DELETE,
  `GenerateCSR`, `Rekey` and `Renew` that return real PKCS#10 requests and keep the key, and `ReplaceCertificate`
  that gives the new certificate its own path. The emulator tree has no CertificateService.
- **Not implemented**: decoding PKCS7 and PKCS12, automatic enrollment, ForceAutomaticRenew, SPDM and UEFI
  properties, encrypted keys (see PARITY.md).

## Query parameters (v0.12.0)

Beyond the Ansible modules, which send none: the query parameters of DSP0266 1.23.0 7.3, to cut the number of requests
to a BMC. The reference is the specification (7.3.1 overview, 7.3.2 `$expand`, 7.3.3 `$select`, 7.3.4 `$filter`, 9.6.10
count; the `ProtocolFeaturesSupported` of the ServiceRoot 1.22.0 schema, DSP8010 2025.4). A client may only use a
parameter the service advertises there, so the root is read once per client (`Client.ProtocolFeatures`) and a
parameter that is not advertised is refused with `ErrUnsupported`, naming the feature, and never sent. Each capability is
a row of the section "Beyond the Ansible modules" of [PARITY.md](PARITY.md).

- **Library** (`query.go`, `expand.go`): `ProtocolFeatures`, `Query{Expand, Select, Top, Skip, Only, Excerpt, Filter}`,
  `Query.Encode`, `Client.GetWith`. Values are validated (`ErrQuery`): `$select` is names joined by slashes, `$filter` is
  parsed with the grammar of 7.3.4 (comparison, `and`, `or`, `not`, parentheses, literals), `only` is never combined
  with another parameter, and a value is percent-encoded so that it cannot add a parameter. The read paths that were N+1
  now send `$expand=.` when the root advertises it (`CPUInventory`, `MemoryInventory`, `NICInventory`,
  `ChassisSensors`, the power and thermal subsystems, `ChassisInventory`, `FirmwareInventory`, `SoftwareInventory`,
  `ListUsers`, `Roles`, `Subscriptions`; since v0.13.0 also `StorageControllerInventory`, `DiskInventory`,
  `VolumeInventory`, `HostInterfaces`, `VirtualMedia`, the LogService resources and the entries of `Logs`, and the
  collections the health reports walk), and read the members one by one when it does not, when `Config.NoExpand` is
  set, or when the expanded answer fails (501, 507, too large): the answers are the same. Against the mock,
  `ChassisSensors` takes 2 requests instead of 43, `CPUInventory` 2 instead of 5, `SystemHealthReport` 11 instead of
  20, `VolumeInventory` 6 instead of 10.
- **CLI**: `redfish raw get PATH --expand N [--expand-kind nolinks|links|all] --select A,B --top N --skip N --only
  --excerpt --filter EXPR`, each refused when it is not advertised; the global `--no-expand` forces the per-member reads of
  the inventory and listing commands (the default is to expand when advertised).
- **Bounds**: an expanded answer may be larger than a plain one, so a request with `$expand` is bounded by
  `MaxExpandedBody` (8 MiB), never unbounded; every other body stays bounded by `MaxBody` (1 MiB).
- **Mock**: the rackmount tree advertises and implements `$expand` (`.`, `~`, `*`, `$levels`), `$select`, `$filter`, `$top`,
  `$skip`, `only` and `excerpt` (the mockups have no Excerpt annotation: the whole resource, as 7.3.1 says); the
  emulator tree, and `NoQueries`, advertise nothing and answer 501 to a `$` parameter and ignore the others, as 7.3.1
  says. `Queries()` logs the raw query of each request.
- **Fuzzed**: `FuzzQueryEncode`, `FuzzParseProtocolFeatures` (committed seeds under `testdata/fuzz`).
- **Not implemented**: `includeoriginofcondition` (it belongs to the event stream), `$skiptoken` (opaque, followed
  through the next link), OEM parameters, `$top` on the log entries (a shorter page need not carry a next link, so the
  entries read could differ), and `$expand` where there is no collection to read: the drives of a Storage (an array of
  links in the resource), the VirtualMedia slots read by insert and eject (each answer's `Allow` header is needed),
  the single link of a health report.

## Telemetry (v0.14.0)

Beyond the Ansible modules, which have no telemetry: the TelemetryService, its metric definitions, metric report
definitions, metric reports and triggers, and the metric reports of the event stream. The reference is the
specification as published: DSP0266 1.23.0 (12.1.6 Table 38, 12.5.2 and 12.5.2.2 the metric report SSE stream) and the
DSP8010 2025.4 schemas TelemetryService 1.4.1, MetricDefinition 1.3.6, MetricReportDefinition 1.4.7, MetricReport 1.5.2,
Triggers 1.4.0 and Schedule 1.2.5. Each capability is a row of the section "Beyond the Ansible modules" of
[PARITY.md](PARITY.md).

- **Library** (`telemetry.go`): `TelemetryService` (status, `SupportedCollectionFunctions`, `MinCollectionInterval`,
  `MaxReports`), `MetricDefinitions`, `MetricReportDefinitions` (type OnRequest, Periodic or OnChange, schedule,
  metrics, report actions), `MetricReports`, `MetricReport`, `Triggers`; `CreateMetricReportDefinition`,
  `DeleteMetricReportDefinition` and `SubmitTestMetricReport`, each write an `Outcome` and is checked against the
  schema's enumerations and the service (interval at least `MinCollectionInterval`, function in
  `SupportedCollectionFunctions`, `MetricId` of a `MetricDefinition`, `MaxReports`, the ActionInfo of the action) before
  anything is sent. A service without the feature gives `ErrUnsupported`.
- **Metric values**: `MetricValue.MetricValue` is a string in the schema. A value keeps the string the service sent
  (`Raw`), a `null` is `Null`, and `Number` is set only when the string is a JSON number that fits a float64
  (`ParseMetricNumber`: no space, plus sign, hexadecimal, `NaN`, leading zero or overflow). A value that is not a number
  is never read as 0 (`MetricValue.Float` returns `ErrNotNumeric`). `MetricReport.TimeSeries` groups the numeric values
  by metric and property and sorts them by timestamp; a value with no number or no RFC 3339 timestamp is returned as
  skipped, not given the report's time.
- **On demand**: a definition of type OnRequest generates its report when the report is read; the schema has no action
  for it, so `MetricReport` is it.
- **Stream**: a metric report sent over SSE (`EventFormatType` MetricReport) is decoded by the existing `EventStream`
  into the same `MetricReport` (`Event.Report`): there is no second parser. `MetricReportEvents` opens the stream with the
  filter `EventFormatType eq 'MetricReport'`.
- **Meter**: `Read` and `Meter` are unchanged. `ReadingsFromSeries` is an opt-in adapter that turns an energy series
  (kWh, Wh or J) and a power series of a report into the `Reading`s a `Meter` observes; the `Meter` still decides what a
  reset or a gap is worth.
- **CLI**: `redfish telemetry status|definitions|report-definitions|reports|report|triggers|create-definition|delete-definition|test|watch`.

```sh
redfish ... telemetry status
redfish ... telemetry report-definitions PowerMetrics
redfish ... -o table telemetry report PowerMetrics        # a time series: METRIC PROPERTY TIME VALUE
redfish ... telemetry create-definition --id Temps --type Periodic --interval PT5S --metric CPU1Temp --function Maximum \
  --action LogToMetricReportsCollection --updates AppendWrapsWhenFull --append-limit 16
redfish ... telemetry watch -o table                      # the metric reports of the event stream
```

- **Bounds and fuzzing**: a report document above 1 MiB, or with more than 4096 values, is refused; a text a report
  keeps is clipped at 4096 bytes; a collection is read for at most 256 members. `FuzzParseMetricReport`,
  `FuzzParseMetricNumber`, `FuzzParseRedfishDuration` and `FuzzParseTelemetryResources` run in the CI fuzz matrix, with
  committed corpora under `testdata/fuzz`.
- **Mock**: both trees get a TelemetryService with three MetricDefinitions, a Periodic definition (PowerMetrics) whose
  report is regenerated from the sensors' `Reading` (`TickTelemetry`; `redfish mock serve --telemetry-interval`), an
  OnRequest one generated by the GET, a Trigger, create and delete of a definition, `SubmitTestMetricReport`, and the
  metric reports on the existing SSE stream (`EventFormatType`).
- **Not implemented**: see PARITY.md (the other actions of the service, trigger writes, wildcards, push delivery).

## What it follows

Each rule comes from a document opened on 2026-11-05 (see the package documentation for the details):
DSP0266 1.19.0 (service root 7.2.3, paged collections 7.2.2, Basic 13.3.3, sessions 13.3.4), Chassis v1.29.0,
EnvironmentMetrics v1.3.1, Sensor v1.6.7 and Power v1.7.3. A counter is preferred to a power: the energy
between two reads is then exact. The `EnvironmentMetrics` of a chassis already covers the chassis it
contains, so `Read` returns the first chassis with a reading and never adds chassis up.

## Safety

https only; certificates verified (a CA file, else the system roots); TLS 1.2 or later; no redirect
followed; every body bounded to 1 MiB (8 MiB for a request with `$expand`); every request under a timeout; credentials resolved per request
through the `Credentials` interface; no error carries a credential; a reading that is null, negative,
not finite or out of range is dropped. Errors are typed (`ErrAuth`, `ErrNotFound`, `ErrTooLarge`,
`ErrMalformed`, `ErrNoReading`, `ErrInsecure`, `*StatusError`). The JSON decoders are fuzzed.

## From reads to energy: Meter

`Meter` turns the successive reads of one BMC into the energy used between them:

- with an energy counter, the difference of two reads (exact, whatever the gap: a counter is cumulative);
- with only a power, the trapezoid (P0 + P1) / 2 × dt between two samples, exact for a constant power. Two
  samples further apart than `MaxGap` (default 5 minutes) are **not** joined: the power in between is
  unknown, the interval is invalid and the caller falls back;
- a counter that goes down (a BMC reboot) is never reported as a negative energy: the interval is invalid
  (`ReasonReset`) and counting restarts from the new value;
- a change of source between two reads (counter, then only power) is invalid too.

An invalid `Interval` holds 0 and says why; it is not a measurement of 0.

## What it does not do

No polling and no attribution: those belong to the caller (`Meter` only differences or integrates the reads it is given). `Read` and `Meter` never
write to a BMC; the Systems commands and the `raw` CLI do, only when you call them. It does not follow `PowerSubsystem` (the Power link it reads is deprecated in
Chassis v1.29.0 but still the one most BMCs serve).

## Status

v0.14.0: the TelemetryService, metric reports and their stream (beyond the Ansible parity); v0.13.0: `$expand` in the storage, host interface, virtual media, log and health reads; v0.12.0: the query parameters, `$expand` in the read paths (beyond the Ansible parity); v0.11.0: the CertificateService and its certificates (beyond the Ansible parity); v0.10.0: the EventService, subscriptions and the SSE stream (beyond the Ansible parity); v0.9.0: the Storage commands (parity 99/99); v0.8.0: the UpdateService commands; v0.7.0: the Accounts and Sessions commands; v0.6.0: the Managers commands, virtual media and logs; v0.5.0: the Chassis commands; v0.4.0: the Systems commands (library, CLI, second mock tree); v0.3.0: the generic client, the mock BMC and the CLI; v0.2.0 and before: the energy reader. Section
numbers cited in the code are DSP0266 1.23.0's.

**Performance.** `BenchmarkGet` (one resource of the mock over TLS keep-alive, client and mock in one process,
Apple M4 Max, one core): 291 allocations per read (exact). **Against redfishtool** (DMTF's CLI, 1.1.8),
one CLI invocation on the same mock, measured 2026-11-06 at load ~4.5 (three rounds, see
[BENCHMARKS.md](BENCHMARKS.md)): a raw GET of the ComputerSystem takes **4.6–5.3 ms** with `redfish`
and **69–75 ms** with `redfishtool`. Most of that gap is the Python interpreter starting on every call.
`redfishtool` also never verifies the BMC's certificate (`verify=False`); `redfish` does, at no measurable cost.

Pre-1.0: the API may change in a minor version. Born in
[go-llmflow/gateway](https://github.com/go-llmflow/gateway), which bills a node's energy from its BMC.
