// Package redfish is a small client for the DMTF Redfish API of a server's BMC,
// to read what a node consumes: the energy counter of its chassis
// (EnvironmentMetrics.EnergykWh), else its power (EnvironmentMetrics.PowerWatts
// or Power.PowerControl[].PowerConsumedWatts).
//
// It is self-contained (the standard library only), safe for concurrent use,
// and does no scheduling: polling, counters over time and attribution belong to
// the caller.
//
// # What it follows
//
// Each rule below comes from a document opened on 2026-11-05.
//
//   - DSP0266 Redfish Specification 1.19.0 (dmtf.org/sites/default/files/
//     standards/documents/DSP0266_1.19.0.pdf): the service root, by default
//     /redfish/v1/ (section 7.2.3); HTTP Basic authentication (13.3.3: "All
//     requests that use HTTP Basic authentication shall require HTTPS"); login
//     sessions (13.3.4: the Sessions collection is found in the service root's
//     Links.Sessions; a POST of {"UserName", "Password"} "without an
//     authentication header", over HTTPS, answers with an X-Auth-Token header,
//     the token to send on later requests, and a Location header, the Session
//     resource to DELETE to log out; the session ID is not the token).
//   - Chassis v1.29.0 (redfish.dmtf.org/schemas/v1/Chassis.v1_29_0.json): the
//     EnvironmentMetrics property links the chassis' EnvironmentMetrics
//     resource, "all containing chassis, and devices contained by any of these
//     chassis instances", with the warning that "care should be taken to ensure
//     any reported values do not overlap or result in double-counting": Read
//     therefore returns the first chassis that has a reading rather than adding
//     chassis up. The Power link is deprecated in favour of PowerSubsystem and is
//     only a fallback here.
//   - EnvironmentMetrics v1.3.1 (EnvironmentMetrics.v1_3_1.json): EnergykWh,
//     "the total energy, in kilowatt-hour units, for this resource"; EnergyJoules,
//     its device-level equivalent; PowerWatts. Each is a Sensor excerpt whose
//     Reading is the value (Sensor v1.6.7, SensorEnergykWhExcerpt). A counter is
//     preferred to a power: the energy between two reads is then exact.
//   - Power v1.7.3 (Power.v1_7_3.json): PowerControl[].PowerConsumedWatts, "the
//     actual power that the chassis consumes, in watt units" (minimum 0).
//
// # Safety
//
// https only, certificates verified (against a CA file when given), TLS 1.2 or
// later, no redirect followed, every body bounded to MaxBody, every request
// under a timeout, credentials resolved per request through Credentials, and no
// error carries a credential. A reading that is null, negative, not finite or
// out of range is dropped by the parsers, which are fuzzed.
package redfish
