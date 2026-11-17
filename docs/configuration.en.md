# Configuration Reference

[한국어](configuration.md) | **English**

The configuration has two layers.

- `upi-forge.yaml` — **common settings** such as cluster, registry, and web
  server
- `pathsets/<name>/` — per-hardware-group disks, network, and node list

Unknown keys are reported immediately as errors with the line number, so a
typo is never silently ignored.

## Path resolution rules

- Relative paths inside a config file → relative to the **directory
  containing the config file**
- `workspace.dir` → relative to the **current working directory** (if empty,
  wherever you ran it)

The recommended layout is to keep the config bundle in one place (e.g.
`/opt/upi-forge/configs/`) and let artifacts accumulate in the working
directory.

## upi-forge.yaml

```yaml
cluster:
  domain: "mycluster.example.com"   # domain of the cluster the worker joins
  apiURL: ""                        # if empty, https://api.<domain>:6443
  version: ""                       # if empty, looked up from the logged-in cluster
  mcsPort: 22623
```

| Key | Description |
|---|---|
| `cluster.domain` | The basis for all validation and the MCS address. A node FQDN must belong to this domain. |
| `cluster.apiURL` | Even when specified, the host must belong to `cluster.domain` (blocks an override pointing at another cluster). |
| `registry.pullSecret` | Path to the pull secret for release images. **Do not commit it to the repository.** |
| `registry.registry` | `quay.io` in connected environments, or the mirror registry in disconnected environments (e.g. `mirror.example.com/ocp4`) |
| `webServer.url`, `webServer.path` | The web server that publishes the generated ISO/rootfs. It must be reachable from iDRAC and the nodes. Only `http`/`https` allowed. |
| `workspace.*` | Artifact file names. You can use the defaults as-is. `tmpDir` must be inside the working directory because prepare deletes the entire directory. |
| `pathsets.active` | The hardware group to work on now. Can be changed temporarily with the `--pathset` global option. |
| `redfish.*` | iDRAC connection options. For self-signed certificate environments, `tlsVerify: false`. `*WaitSeconds` are the wait times between power/media operations. `powerOffWaitSeconds` is the maximum wait for polling the power-off confirmation; it proceeds immediately once power-off is confirmed, and proceeds with a warning even if it is not confirmed within the time. |
| `tools.*` | External command paths. If `coreosInstaller` is empty, prepare uses the one it extracted. |
| `logger.debug` | If `true`, verbose logging — the external commands that run (including arguments) and Redfish requests are printed as they are. Can also be turned on temporarily with the `--debug` global option. |

## pathset.yaml

The configuration of a single hardware group. **Nodes in the same group must
have an identical disk by-path and NIC layout.** The directory name and
`name` must match.

```yaml
name: "baremetal-example"
description: "bare metal / iDRAC10 / NIC bonding / NVMe RAID1"

iso:
  useRootfs: false        # default ISO mode (change per run with --full/--rootfs)

disk:
  osDisk: "/dev/disk/by-path/pci-0000:3c:00.0-nvme-1"   # measured value

butane: "baremetal-example.bu"   # optional: extra disks/RAID, etc.

network:
  source: "generate"      # generate | copy
  gateway: "192.0.2.1"
  prefixLength: 24
  dns: ["192.0.2.10", "192.0.2.11"]
  bonding: true
  bond:
    name: "bond0"
    mode: "active-backup"
    primary: "eno1np0"
    standby: "eno2np1"
    miimon: 100

bmc:
  type: "idrac10"           # defaults to idrac10 when omitted; idrac9 is also supported.
  samePassword: false       # if true, enter one shared password once
  parallel: 0               # if 2 or more, process that many nodes concurrently
```

| Key | Description |
|---|---|
| `disk.osDisk` | The by-path of the disk where the OS is installed. **Never include it in a Butane wipe/RAID target.** Measure it with a live boot down to the suffix (`-nvme-1`, etc.). |
| `butane` | If specified, it is rendered with `butane` and merged into the worker Ignition. The supported merge scope is `storage.{disks,raid,filesystems,files,directories,links,luks}` and `systemd.units`; if any other field (`passwd.users`, `kernelArguments`, etc.) is present, it stops with an error instead of being silently dropped. Defining a file path the same as the template's (`/etc/hostname`, etc.) is rejected at generation time. |
| `network.source` | `generate`: build NMState from nodes.csv/nics.csv and the values above (IPv4 static IP). `copy`: use a pre-made `network-yaml/<hostname>.yaml` as-is (for special setups like IPv6). |
| `network.dns` | The DNS servers the nodes will use. The ignition DNS validation also queries these servers directly. Can be omitted in copy mode (DNS validation is skipped when omitted). |
| `network.bonding` | If `true`, active-backup bonding with the two `bond.*` NICs; if `false`, uses only the single `activeNIC`. |
| `bmc.type` | The management controller type in idracs.csv. `boot`/`live-boot`/`eject`/`inventory` select the implementation by this value. Supports `idrac10` (default, verified on real hardware) and `idrac9` (based on the field script procedure; Go driver hardware verification planned). iDRAC9 configures the one-time boot via SCP Import (it powers on only after the import job completes, and aborts on job failure or timeout), auto-selects the virtual media location and action paths from the values the machine advertises, and — unlike iDRAC10 — does not change virtual media attributes (encryption, etc.). On firmware without a boot-order Settings resource, it warns and skips the step that excludes the virtual CD. iDRAC9 inventory collection uses the same query path as iDRAC10, and the results are for reference until confirmed by a live-boot survey. `eject --address` specifies the type via `--bmc-type`. |
| `bmc.samePassword` | If `true`, it assumes all nodes use the same BMC password, shows all the target nodes and the work to be done, and prompts once. It is independent of `parallel`, so it applies even to sequential runs (process one at a time, but prompt once). Per-node environment variables (`UPI_FORGE_IDRAC_PASSWORD_<HOSTNAME>`) are ignored in this case. Defaults to `false` (per-node prompt) when omitted. |
| `bmc.parallel` | The **upper bound** on the number of nodes a BMC command processes concurrently. `0`/`1` (default) means sequential; `2` or more collects all passwords first and then processes that many concurrently. If there are fewer targets than the bound, only as many as the target count run concurrently (3 machines + `parallel: 4` → 3 concurrent), and each time one finishes the next waiting node continues. If there is only one target, it runs sequentially regardless of the value. In parallel mode, per-node detailed output goes to the pathset's `logs/<command>-<run-time>-<unique>/<hostname>.log`, and only the overall progress is shown on screen. Even if some nodes fail, the rest proceed to the end, and at the end it tells you the command to re-run only the failed nodes (e.g. `upi-forge boot worker5 worker7`). |

## CSV files

All three files: UTF-8 BOM and CRLF allowed, the header row is optional (if
written, use the exact names); extra columns, duplicates, and empty values
are errors. **Row order is processing order.**

### nodes.csv — `hostname,ip`

```csv
hostname,ip
worker4.mycluster.example.com,192.0.2.111
worker5.mycluster.example.com,192.0.2.112
```

- The hostname is an FQDN or a short name. **If it is an FQDN, the domain
  must match `cluster.domain`.** A short name has the domain appended for DNS
  validation, but the node's `/etc/hostname` gets exactly what you wrote — if
  the existing nodes use FQDN names, writing FQDNs is recommended.
- The ip is the static IPv4 address to assign to the node.

### nics.csv — one NIC name per line

```csv
nic
eno1np0
eno2np1
```

The list of measured NIC names. The `activeNIC`/`bond.primary`/`bond.standby`
in `pathset.yaml` must be in this list. NICs that are in the list but not
used are explicitly set down and included in the NMState.

### idracs.csv — `hostname,idrac_ip,idrac_id` (bare metal only)

```csv
hostname,idrac_ip,idrac_id
worker4.mycluster.example.com,203.0.113.111,root
worker5.mycluster.example.com,203.0.113.112,root
```

- The hostname must have a **one-to-one correspondence** with nodes.csv
  (prevents booting the wrong machine).
- **Passwords are not recorded.** They are entered at runtime or taken from
  environment variables.
- A pathset with this file is treated as bare metal; without it, as a VM.

## Environment variables

| Variable | Purpose |
|---|---|
| `UPI_FORGE_CONFIG` | Config file path to use instead of `--config` |
| `UPI_FORGE_IDRAC_PASSWORD` | Shared password for all iDRACs |
| `UPI_FORGE_IDRAC_PASSWORD_<HOSTNAME>` | Per-node password (takes precedence over the shared one). Replace `.` and `-` in the hostname with `_` and uppercase it |
