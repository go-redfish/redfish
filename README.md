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

The next releases bring one area each (Chassis, Managers, Accounts, Update, Storage/Events/Certificates),
each adding its library functions, CLI commands and PARITY.md rows.

## What it follows

Each rule comes from a document opened on 2026-11-05 (see the package documentation for the details):
DSP0266 1.19.0 (service root 7.2.3, paged collections 7.2.2, Basic 13.3.3, sessions 13.3.4), Chassis v1.29.0,
EnvironmentMetrics v1.3.1, Sensor v1.6.7 and Power v1.7.3. A counter is preferred to a power: the energy
between two reads is then exact. The `EnvironmentMetrics` of a chassis already covers the chassis it
contains, so `Read` returns the first chassis with a reading and never adds chassis up.

## Safety

https only; certificates verified (a CA file, else the system roots); TLS 1.2 or later; no redirect
followed; every body bounded to 1 MiB; every request under a timeout; credentials resolved per request
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

v0.4.0: the Systems commands (library, CLI, second mock tree); v0.3.0: the generic client, the mock BMC and the CLI; v0.2.0 and before: the energy reader. Section
numbers cited in the code are DSP0266 1.23.0's.

**Performance.** `BenchmarkGet` (one resource of the mock over TLS keep-alive, client and mock in one process,
Apple M4 Max, one core): 291 allocations per read (exact). The time and the comparison with redfishtool on the
same mock are **deferred**: the machine's 1-minute load was 12.6, above the limit of 8 for a measurement.

Pre-1.0: the API may change in a minor version. Born in
[go-llmflow/gateway](https://github.com/go-llmflow/gateway), which bills a node's energy from its BMC.
