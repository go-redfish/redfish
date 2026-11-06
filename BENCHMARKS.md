# Benchmarks: `redfish` vs DMTF `redfishtool`

What one command costs, end to end, against the same Redfish service. The
reference is DMTF's own CLI client,
[`redfishtool`](https://github.com/DMTF/Redfishtool) **1.1.8** (PyPI). Ansible's
`community.general` modules shell out to it in go-ansible's default backend.

**Measured 2026-11-06**, Apple M4 Max, macOS, 1-minute load 4.1–5.1. Versions:
`redfish` v0.9.0 (go1.27.3, `GOMAXPROCS=1`), Python 3.13 in a `uv` venv, and
hyperfine 1.20.0. Each command had 3 warm-up runs and 20 measured runs.
There were **three rounds**, the second in reverse order, so that a drift in
machine load shows up as a spread instead of hiding in one tool's numbers.

## The service

Both tools talk to `redfish mock serve` (the DMTF `public-rackmount1`
mockup, see [mockbmc](mockbmc/)) over HTTPS on loopback, with HTTP Basic
authentication. The mock runs as a separate process. The same mock instance
served every run of every round.

## What is compared

| Name | Command |
|---|---|
| `rt-systems` | `redfishtool -q -r HOST -c rt.json -S Always Systems -1 get` |
| `go-systems` | `redfish --endpoint https://HOST --ca-file mock-ca.pem --user admin --password-file pw systems inventory system` |
| `rt-raw` | `redfishtool -q -r HOST -c rt.json -S Always raw GET /redfish/v1/Systems/437XR1138R2` |
| `go-raw` | `redfish … raw get /redfish/v1/Systems/437XR1138R2` |
| `*-insecure` | the `redfish` commands with `--insecure` instead of `--ca-file` |

The **raw GET** pair is the like-for-like comparison: both print the whole
ComputerSystem (177 and 179 lines; `redfish` adds the ETag line). The
**systems** pair does what each tool does to "get the system". Both discover
the member through the collection. `redfishtool` prints the resource, and
`redfish systems inventory system` prints the Ansible module's summary of it.

**TLS is not equal by default.** `redfishtool` never verifies the BMC's
certificate: every request passes `verify=False`
(`redfishtoolTransport.py` lines 256, 392 and 640 in 1.1.8). `redfish`
verifies it by default (`--ca-file` here). The `--insecure` rows are the
like-for-like case. They show verification costs nothing measurable here.

## Results (wall clock, mean ± σ, ms)

| Command | round 1 | round 2 (reversed) | round 3 |
|---|---|---|---|
| `rt-systems` | 75.9 ± 1.4 | 82.8 ± 1.4 | 82.4 ± 7.0 |
| `go-systems` | 5.6 ± 0.5 | 5.6 ± 0.2 | 5.9 ± 0.4 |
| `go-systems-insecure` | 4.9 ± 0.2 | 5.6 ± 0.2 | 6.4 ± 0.3 |
| `rt-raw` | 69.0 ± 1.9 | 74.6 ± 1.8 | 75.3 ± 2.1 |
| `go-raw` | 4.6 ± 0.3 | 5.3 ± 0.2 | 5.2 ± 0.4 |
| `go-raw-insecure` | 4.6 ± 0.2 | 5.9 ± 0.6 | 5.0 ± 0.3 |

**Reading it.** Most of `redfishtool`'s time is the Python process itself:
54–66 ms of **user** CPU per run, against 2.2–2.9 ms for `redfish`. That is
interpreter start-up and imports, paid on every command. A playbook that
calls the tool once per task pays it once per task. Two requests through
loopback TLS cost little next to it.

**What this does not measure.** It does not measure throughput of a long-lived
client making many requests (the library, without a new process per call),
a real BMC, whose own latency (often 100 ms or more per request) would dominate
both columns, or memory. Treat it as the cost of one CLI invocation and
nothing more.

## Reproducing this

```sh
uv venv venv && uv pip install --python venv/bin/python redfishtool
redfish mock serve --listen 127.0.0.1:18446 --ca-out mock-ca.pem &
printf '{"user": "admin", "password": "password"}' > rt.json   # the mock's test password
printf 'password' > pw && chmod 600 pw rt.json
GOMAXPROCS=1 hyperfine -N -w 3 -r 20 \
  -n rt-raw "venv/bin/redfishtool -q -r 127.0.0.1:18446 -c rt.json -S Always raw GET /redfish/v1/Systems/437XR1138R2" \
  -n go-raw "redfish --endpoint https://127.0.0.1:18446 --ca-file mock-ca.pem --user admin --password-file pw raw get /redfish/v1/Systems/437XR1138R2"
```

Check the 1-minute load first. Above about 8, the numbers measure the
machine, not the tools.
