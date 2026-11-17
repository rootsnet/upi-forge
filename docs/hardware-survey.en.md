# Pre-install Hardware Survey

[한국어](hardware-survey.md) | **English**

The NIC names and disk by-paths in the pathset configuration must be **the
values the operating system actually assigns**. They can differ from the
names on the firmware screen or in documentation, so for a new hardware group
survey them **first of all** in the whole process, and write `pathset.yaml`
and `nics.csv` with those values.

The method is "boot the base image (the shared `coreos-x86_64.iso`) and
check." Because this is a survey, not an installation, per-node artifacts
(`<hostname>.ign`, `<hostname>.iso`) are not needed, and the pathset's
undetermined values (osDisk, NIC) may be placeholders.

## Bare metal (iDRAC9 / iDRAC10)

Specify the hardware generation with the pathset's `bmc.type` (defaults to
`idrac10` when omitted). iDRAC9 inventory uses the same query path as
iDRAC10, so the results are for reference until confirmed by a `live-boot`
survey.

### 1. inventory — collect iDRAC reference information

```bash
upi-forge inventory
```

This queries and saves the NICs (MAC, link state, speed) and storage
(controller, PCI address) that iDRAC reports. It **only queries (GET)** and
does not change server settings.

Output: `<pathset>/inventory/<hostname>/nic-inventory.{yaml,json}`,
`storage-inventory.{yaml,json}`

Both documents include a schema version (`schema_version` / `SchemaVersion`).
The storage inventory keys use vendor-neutral names (`BmcAddress`,
`SlotType`, `ControllerId`).

Some cards advertise a port link via the BMC that does not actually exist
(HTTP 404), which can produce a port-query warning. Collection continues, and
that port is filled in from the device function values and (if present)
supplementary EthernetInterfaces values. If there is no supplementary
information, some values may be empty —
see [troubleshooting.en.md](troubleshooting.en.md#inventory).

Note: the storage by-path value is an **expected value** confirmed only down
to the PCI address. The suffix (`-nvme-1`, etc.) is attached by the kernel,
so always confirm it with a live boot.

### 2. live-boot — boot the base image and measure

After publishing the full ISO made by `prepare` to the web server:

```bash
upi-forge live-boot
upi-forge --pathset <name> live-boot    # to survey a group that is not active
```

This boots each node from the shared ISO via iDRAC Virtual Media. Connect
with the iDRAC Virtual Console (entering just the emergency mode is enough)
and check the following.

```bash
ip -br link                 # NIC names and link state
ls -l /sys/class/net/       # NIC ↔ PCI address mapping
ls -l /dev/disk/by-path/    # disk by-path (osDisk, Butane targets)
```

Cross-referencing with the MAC addresses from the inventory lets you confirm
which name corresponds to which physical port.

### 3. Apply to the configuration

Write `pathset.yaml` (osDisk, activeNIC/bond) and `nics.csv` with the
measured values, then proceed to the `ignition` step. The node used for the
survey has nothing installed yet, so you can leave it as is.

## VM

`live-boot` is iDRAC-only, so it does not apply to VMs. Mount the base image
as a virtual CD/DVD in the hypervisor, boot it, and check with the same
commands as above. Because VMs cloned from the same template have an
identical layout, checking just one is often enough.

## Full sequence summary

```text
prepare → publish full ISO → [bare metal: inventory] → boot base image and measure
        → write pathset.yaml/nics.csv → ignition → iso → boot (or manual mount)
```
