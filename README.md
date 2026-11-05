# redfish

[![Go Reference](https://pkg.go.dev/badge/github.com/go-redfish/redfish.svg)](https://pkg.go.dev/github.com/go-redfish/redfish)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue.svg)](LICENSE)
[![CI](https://github.com/go-redfish/redfish/actions/workflows/ci.yml/badge.svg)](https://github.com/go-redfish/redfish/actions/workflows/ci.yml)

**Part of [go-redfish](https://github.com/go-redfish)**: Go libraries for the Redfish API of a server's BMC. Documentation: <https://go-redfish.github.io/docs/>.

A small, self-contained Go client for the [DMTF Redfish](https://www.dmtf.org/standards/redfish)
API of a server's BMC, to read **what a node consumes**: the energy counter of its chassis
(`EnvironmentMetrics.EnergykWh`), else its power. Standard library only.

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

No polling and no attribution: those belong to the caller (`Meter` only differences or integrates the reads it is given). It writes nothing to a BMC
except a session login. It does not follow `PowerSubsystem` (the Power link it reads is deprecated in
Chassis v1.29.0 but still the one most BMCs serve).

## Status

Pre-1.0: the API may change in a minor version. Born in
[go-llmflow/gateway](https://github.com/go-llmflow/gateway), which bills a node's energy from its BMC.
