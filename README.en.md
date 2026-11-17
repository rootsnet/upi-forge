# UPI Forge

[한국어](README.md) | **English**

A tool that automates **adding worker nodes to an OpenShift cluster the UPI
way**. From preparing the RHCOS image, to generating per-node
Ignition/NMState, to building per-node install ISOs, to booting via
iDRAC9/iDRAC10 Redfish Virtual Media — all handled by a single executable.

```text
prepare → ignition → iso → (publish to web server) → boot
```

- **Disconnected environments first**: no external library dependencies, so
  it builds from source alone on a bastion with no internet. At runtime it
  needs no external commands beyond `oc`, `butane`, and `coreos-installer`.
- **Accident-preventing validation before execution**: it verifies that a
  node hostname matches the cluster domain, that DNS forward/reverse records
  are registered, and the provenance of existing artifacts (which node, which
  cluster they are for) before running — preventing accidents like booting a
  node of a different cluster or installing with a stale certificate.
- **Shared by VMs and bare metal**: the same flow supports both VMs (manual
  ISO mount) and bare metal (iDRAC9/iDRAC10 automatic boot).

> The example domain (`mycluster.example.com`) and IPs (`192.0.2.x`,
> `203.0.113.x` — documentation-reserved ranges) in this repository are all
> placeholders. Replace them to match your environment.

## Requirements

| Item | Description |
|---|---|
| Go | 1.23 or later (needed only to build) |
| `oc` | Resolving and extracting the release image, cluster pre-flight checks |
| `butane` | Needed only when a pathset contains a `.bu` file |
| `coreos-installer` | `upi-forge prepare` prepares it automatically from the release image |
| Web server | Serves the generated ISO and rootfs to iDRAC (bare metal / rootfs mode) |
| iDRAC | **iDRAC10**: verified on real hardware (PowerEdge R670). **iDRAC9**: a driver that ports a field-verified Redfish script procedure; verification of the Go driver itself on real hardware is planned. The generation is selected by the pathset's `bmc.type`, defaulting to `idrac10`. iDRAC8 and earlier take different Redfish paths and are not guaranteed to work |

`jq`, `openssl`, `curl`, `python3`, and the like are not required.
The runtime target is Linux (Windows/macOS are supported for build and
development only).

## Installation

Pre-built linux-amd64 executables are available from
[Releases](../../releases). The tar.gz contains the executable, an example
`configs/`, and this README (verify integrity with `SHA256SUMS`). To build it
yourself, follow the steps below.

## Build

```bash
make
```

This runs the checks and tests and produces `bin/upi-forge`.

Targets for occasional use: `make build` (skip checks, build only),
`make dist` (executable + configs as a distributable tar.gz),
`make help` (full list).

Even when bringing it into a disconnected environment, a single
`go build ./cmd/upi-forge` after copying the repository is enough. There is
no module-download step.

## Quick start

```bash
# 1. Prepare the configuration: copy configs/ and fill in the domain, IPs,
#    and pathset values. (see docs/configuration.md)

# 2. Point to the config path with an environment variable and check it
#    (no outbound connection)
export UPI_FORGE_CONFIG=/opt/upi-forge/configs/upi-forge.yaml
upi-forge config

# 3. In the working directory where artifacts will accumulate, prepare the
#    image and tools
cd /opt/upi-work
upi-forge prepare

# 4. (Bare metal) Survey the hardware → write pathset.yaml/nics.csv
upi-forge inventory        # iDRAC reference information
upi-forge live-boot        # boot the base image to measure NICs/disks

# 5. Generate per-node Ignition/NMState (includes DNS/hostname validation)
upi-forge ignition

# 6. Generate per-node install ISOs (includes provenance validation of
#    existing artifacts)
upi-forge iso

# 7. Publish, then boot
install -m 0644 ./*.iso /var/www/html/iso/
upi-forge boot             # bare metal: iDRAC Virtual Media boot
                           # VM: mount the generated ISO as a virtual CD/DVD

# 8. Clean up virtual media after installation
upi-forge eject
```

Instead of the environment variable, you may pass `--config <path>` on each
command.

## Documentation

| Document | Contents |
|---|---|
| [docs/getting-started.en.md](docs/getting-started.en.md) | The entire workflow from start to finish |
| [docs/configuration.en.md](docs/configuration.en.md) | Configuration file and CSV reference |
| [docs/hardware-survey.en.md](docs/hardware-survey.en.md) | Pre-install hardware survey (inventory / live-boot) |
| [docs/validation.en.md](docs/validation.en.md) | How the pre-flight validations work and how to skip them |
| [docs/troubleshooting.en.md](docs/troubleshooting.en.md) | Common errors and how to handle them |

## Command summary

| Command | Role |
|---|---|
| `upi-forge config` | Inspect the loaded configuration and computed paths |
| `upi-forge prepare` | Prepare the RHCOS ISO/coreos-installer, split out the minimal ISO and rootfs |
| `upi-forge ignition` | Generate per-node NMState YAML and Ignition |
| `upi-forge iso [--full\|--rootfs] [NODE ...]` | Generate per-node install ISOs |
| `upi-forge boot [--from HOST] [NODE ...]` | Boot the install via iDRAC Virtual Media |
| `upi-forge inventory [--from HOST] [NODE ...]` | Collect the iDRAC NIC/storage inventory |
| `upi-forge live-boot [--from HOST] [NODE ...]` | Live-boot from the shared ISO (for hardware survey) |
| `upi-forge eject [--from HOST] [NODE ...]` | Detach and clean up virtual media (use `--address IP [--bmc-type idrac9]` to target a single machine directly) |
| `upi-forge version` | Show the build version |

Each command provides detailed help via `upi-forge <command> -h`.
The BMC commands (boot/live-boot/eject/inventory) can process only specific
nodes via NODE arguments, and support the pathset's `bmc.samePassword`
(enter one shared password once) and `bmc.parallel` (process nodes
concurrently) — see [docs/configuration.en.md](docs/configuration.en.md).
`--from HOST` and `NODE ...` are mutually exclusive and cannot be used
together.
Turning on the global option `--debug` prints the external commands that run
and the Redfish requests as they are (excluding tokens and passwords) —
see [docs/troubleshooting.en.md](docs/troubleshooting.en.md).

## Safety rules

Rules tied directly to operational accidents are enforced by the tool.

- Existing Ignition and per-node ISOs are **never overwritten** (reuse only
  when the contents are identical).
- If a node hostname is an FQDN, its domain must match `cluster.domain`
  — this prevents accidentally using another cluster's nodes.csv.
- It checks the one-to-one correspondence of hostnames between `nodes.csv`
  and `idracs.csv` — this prevents booting the wrong machine.
- Existing Ignition is compared against the current cluster down to the
  hostname, MCS address, and MCS certificate — filtering out stale artifacts
  of a rebuilt cluster.
- The Virtual CD/DVD is excluded from the boot order to prevent an infinite
  reboot loop after installation.
- The iDRAC password is not stored in a file but entered at runtime
  (automation uses the environment variable `UPI_FORGE_IDRAC_PASSWORD`).

For detailed behavior, see [docs/validation.en.md](docs/validation.en.md).

## License

Apache License 2.0 — see [LICENSE](LICENSE).
