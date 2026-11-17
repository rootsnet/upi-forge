# Full Workflow

[한국어](getting-started.md) | **English**

The complete process of adding worker nodes to an OpenShift cluster with UPI
Forge. The example domains and IPs are placeholders, so read them as the
equivalents for your environment.

- Cluster domain: `mycluster.example.com`
- Node network: `192.0.2.0/24` (gateway `.1`, DNS `.10`/`.11`)
- iDRAC management network: `203.0.113.0/24`
- ISO-publishing web server: `http://192.0.2.50:8080/iso`

## 0. Prerequisites

- You must be logged in to the target cluster with `oc` on the bastion
  (`oc whoami --show-server` returns `https://api.mycluster.example.com:6443`).
- Register the **DNS forward (A) record** of the node to add in advance.
  The reverse (PTR) record is optional, but if registered it must match the
  forward record.
- Prepare the pull secret file used to pull the release image.

## 1. Write the configuration

After copying `configs/` to a location of your choice (e.g.
`/opt/upi-forge/configs/`):

1. `upi-forge.yaml` — fill in the cluster domain, registry, and web server
   address.
2. `pathsets/<name>/` — create a directory per hardware group (pathset).
   Nodes in the same pathset must have an **identical disk by-path and NIC
   layout**.
   - `pathset.yaml` — disks, network (whether bonded), optional Butane
   - `nodes.csv` — `hostname,ip`
   - `nics.csv` — list of NIC names to use
   - `idracs.csv` — (bare metal only) `hostname,idrac_ip,idrac_id`
3. Select the group to work on via `pathsets.active` in `upi-forge.yaml`.
   To change it just once, use the global option `--pathset <name>`.

For field details, see [configuration.en.md](configuration.en.md).

```bash
upi-forge --config /opt/upi-forge/configs/upi-forge.yaml config
```

This confirms which cluster, pathset, and paths are used, with no outbound
connection. Subsequent examples assume the `UPI_FORGE_CONFIG` environment
variable is set.

```bash
export UPI_FORGE_CONFIG=/opt/upi-forge/configs/upi-forge.yaml
```

## 2. Prepare the image and tools — `prepare`

Run this in the working directory where artifacts will accumulate.

```bash
mkdir -p /opt/upi-work && cd /opt/upi-work
upi-forge prepare
```

This extracts the RHCOS full ISO and `coreos-installer` from the release
image, and splits out the minimal ISO and the rootfs image.

Generated files: `coreos-x86_64.iso`, `coreos-installer`,
`coreos-x86_64-minimal.iso`, `coreos-x86_64-rootfs.img`

- If `cluster.version` is empty, the version of the logged-in cluster is
  looked up automatically.
- If you will use only the full ISO, you can skip the split with
  `--skip-rootfs-split`.

## 3. (Bare metal) Survey the hardware

The NIC names and disk by-paths that go into the pathset configuration must
be **the values the OS actually assigns**. For a new hardware group, survey
them first — the procedure is in
[hardware-survey.en.md](hardware-survey.en.md).

For VMs, mount the base image (`coreos-x86_64.iso`) from the hypervisor and
check the same way.

## 4. Generate per-node Ignition/NMState — `ignition`

```bash
upi-forge ignition
```

For every node in the pathset's nodes.csv, this creates `<hostname>.yaml`
(NMState) and `<hostname>.ign` (Ignition). Before generating, it
automatically validates the following.

- The node hostname matches the cluster domain
- DNS forward/reverse registration ([validation.en.md](validation.en.md))
- The `oc` login target matches the cluster in the configuration

The MCS certificate chain is read directly from the cluster at runtime and
embedded in the Ignition; it is not stored separately as a file. If the
pathset has a `.bu` (Butane) file, it is rendered and merged (RAID, extra
mounts, etc.).

If an existing `<hostname>.ign` is present, it stops without overwriting. To
regenerate, delete that file directly and run again.

## 5. Generate per-node install ISOs — `iso`

```bash
upi-forge iso                                  # uses the pathset's iso.useRootfs
upi-forge iso --rootfs                         # rootfs-split mode for this run only
upi-forge iso --full worker1.mycluster.example.com   # a specific node only
```

This creates a per-node `<hostname>.iso`. The Ignition, NMState, and install
disk configuration are embedded in the ISO, so it installs automatically on
boot.

- **full mode**: installation completes with a single ISO.
- **rootfs-split mode**: the ISO is small, but the booted node must download
  `coreos-x86_64-rootfs.img` from the web server (a web server is required
  even for VM targets).

Before running, it validates the provenance (target node, cluster,
certificate) of the existing `<hostname>.ign` — for details, see
[validation.en.md](validation.en.md).

## 6. Publish and boot

### Bare metal (iDRAC9 / iDRAC10)

The pathset's `bmc.type` sets the hardware generation (defaults to `idrac10`
when omitted; `idrac9` for iDRAC9 machines). The procedure is the same for
both generations, and the tool handles the Redfish path differences — see
`bmc.type` in [configuration.en.md](configuration.en.md).

```bash
# Publish to the web server
install -m 0644 ./*.iso /var/www/html/iso/
install -m 0644 coreos-x86_64-rootfs.img /var/www/html/iso/   # when in rootfs mode

# iDRAC Virtual Media boot
upi-forge boot
```

For each node, it prompts for the iDRAC password (not shown on screen),
attaches the virtual media, and starts the install with a one-time boot. If
it fails partway, it tells you the command to resume (`--from <hostname>`).

```bash
upi-forge boot --from worker5.mycluster.example.com   # resume from the failed node
upi-forge boot --skip-iso-check                       # when skipping the ISO URL pre-check
```

### VM

Mount the generated `<hostname>.iso` as the target VM's virtual CD/DVD in the
hypervisor and boot. If the pathset has no idracs.csv, the `iso` command
treats it as a VM target and prints the necessary guidance.

## 7. After installation

```bash
# Detach virtual media (prevents rebooting back into the ISO)
upi-forge eject
# To target a single machine directly without CSVs (iDRAC9 machines require --bmc-type idrac9)
upi-forge eject --address 203.0.113.21 --bmc-type idrac9

# Approve the node join (standard UPI procedure)
oc get csr
oc adm certificate approve <csr-name>    # approve Pending CSRs, usually twice
oc get nodes
```

Detach the virtual CD/DVD from a finished VM as well.

## Automation tips

- In environments where entering the iDRAC password interactively is
  difficult, use environment variables: shared `UPI_FORGE_IDRAC_PASSWORD`, or
  per-node `UPI_FORGE_IDRAC_PASSWORD_<HOSTNAME>` (replace `.` and `-` in the
  hostname with `_`, in uppercase: `worker4.mycluster.example.com` →
  `UPI_FORGE_IDRAC_PASSWORD_WORKER4_MYCLUSTER_EXAMPLE_COM`).
- When moving between multiple clusters, keep a per-cluster config directory
  and change only `UPI_FORGE_CONFIG`. Pre-flight validation blocks wrong
  combinations, but it is better not to mix them in the first place.
