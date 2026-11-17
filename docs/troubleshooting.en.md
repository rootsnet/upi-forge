# Common Errors and How to Handle Them

[한국어](troubleshooting.md) | **English**

The error messages themselves guide you to the items to check, but here is
the per-situation background as well. For validation-related errors
(domain / DNS / artifact provenance), see
[validation.en.md](validation.en.md) first.

## Configuration / startup stage

**`cluster.domain is empty` / `unknown key` (with line number)**
A required value is missing from the config file, or a typo. Typos are not
silently ignored — they are all caught as errors. Check the loaded values
with `upi-forge config`.

**`pathsets.active differs from the name in pathset.yaml`**
The pathset directory name and the `name` in `pathset.yaml` must match.

**`workspace.tmpDir points outside the working directory`**
`tmpDir` is a path that prepare deletes entirely, so it is restricted to
inside the working directory (including the case of a symlink pointing
outside). Check the configuration.

## prepare

**`oc command not found`**
`oc` is not on PATH, or the `tools.oc` path is wrong.

**`the current oc login target differs from the configuration`**
You are logged in to a different cluster. Log in with `oc login` and run
again.

**release image extraction failure**
Check the pull secret path (`registry.pullSecret`) and the mirror registry
address (`registry.registry`). In a disconnected environment, the mirror
registry must have the release image for that version.

## ignition

**`cannot connect to MCS: api-int.mycluster.example.com:22623`**
The bastion must be able to reach the MCS (22623/tcp) to collect the
certificate. Check the firewall and the `api-int` DNS registration.

**`Butane render failed`**
A syntax error in the pathset's `.bu`, or the `butane` version does not match
the spec. Check it directly with `butane --strict <file>`.

**`duplicate path in storage.files`**
The pathset Butane defined a file that is the same as the template's (e.g.
`/etc/hostname`). Remove that item from the Butane.

**`already exists` (existing .ign/.yaml)**
This is the no-overwrite rule. Delete the artifact of the node you want to
regenerate and run again. The same applies when re-running after a partial
failure — delete the files of nodes that were already generated correctly, or
split the nodes to keep into a new pathset.

## iso

**`no full ISO. Run upi-forge prepare first`**
The working directory is different, or prepare was skipped. Check that you are
running in the directory that has the artifacts.

**`the rootfs URL required for rootfs-split mode is empty`**
rootfs mode requires the web server configuration (`webServer`) (even for VM
targets).

**existing Ignition provenance errors (`hostname differs from the target
node`, etc.)**
See the artifact provenance validation section of
[validation.en.md](validation.en.md). The principle is "delete that node's
existing artifacts → regenerate starting from `ignition`."

## inventory

**`the port resource advertised by the BMC does not exist (HTTP 404)` warning**
Some NIC cards or firmware do not actually provide the port resource they
advertise in the device functions, and return HTTP 404. In this case
collection continues: that port uses the device function values, and if there
is a matching EthernetInterfaces entry it supplements the link state and
speed. Items for which no supplementary information is found may have some
empty values, so confirm the final NIC information with a `live-boot` survey
as per the original procedure. Errors other than 404
(authentication/permission/server errors) are treated as real problems and
stop collection.

## boot / live-boot / eject

**`ISO URL pre-reachability check failed`**
Check the web server address and firewall. The web server must be reachable
from the iDRAC management network (GET Range request, HTTP 200/206). If the
check is difficult due to environment characteristics, use
`--skip-iso-check`.

**`Redfish session creation failed (HTTP 401/403)`**
Check the iDRAC account, password, account lockout, and Login privilege. If
you use the password environment variables (`UPI_FORGE_IDRAC_PASSWORD*`),
check the variable naming rule (uppercase, `.`→`_`).

**`HTTP 429` (session count limit)**
The iDRAC's active sessions are full. Clear unused sessions in the iDRAC web
UI. UPI Forge attempts to delete its own session on normal exit, on error,
and on Ctrl+C interruption, and warns if the deletion is not confirmed. If
this warning appears or the session count does not drop, check the BMC's
active sessions directly. UPI Forge cannot see sessions left by other tools.

**`failed to query the Virtual Media device list … (HTTP 404)`** (iDRAC9)
If you target an iDRAC9 machine with `eject --address` but omit
`--bmc-type idrac9`, it queries the iDRAC10 path and gets a 404. On the CSV
path, check the pathset's `bmc.type`. The iDRAC9 driver tries the
System-advertised path → the `Systems` standard path → the `Managers` legacy
path in order, moving to the next candidate only on a 404; other errors
(authentication/server errors) are treated as real problems and stop.

**`the SCP Import job did not finish …` / `… failed`** (iDRAC9)
iDRAC9's one-time boot setting is done via an SCP Import job. The tool powers
on only after the job status is `Completed` and, on firmware that provides
`PercentComplete`, after progress reaches 100%. If the job fails
(`Exception`, `CompletedWithErrors`, `TaskStatus=Critical`, etc.) or does not
finish within 5 minutes, it does not power on and aborts. Check the job path
shown in the message (`/redfish/v1/TaskService/Tasks/JID_…`) in the iDRAC Job
Queue, clean it up, and re-run only that node (`upi-forge boot <hostname>`).
At the point of abort the server is left powered off, and a re-run does not
send another power-off to an already-off server, so you can just continue. If
the job query returns 401/403, check the iDRAC account privileges (SCP Import
requires the Configure privilege).

**`failed to confirm the boot setting`** (iDRAC9)
On firmware where the job location cannot be known, the path that confirms by
reading the result attributes (DellAttributes) failed due to an
authentication/server error or a slow response. Increase
`redfish.attributeWaitSeconds` or check the iDRAC state and run again. Older
firmware that has no confirmation path at all (HTTP 404) only leaves a
warning and proceeds, so in that case confirm the ISO boot directly in the
iDRAC Virtual Console.

**`no boot-order Settings resource, so … skipped`** (iDRAC9)
The protective step of removing the virtual CD/DVD from the normal boot order
cannot be performed on this firmware, so it was skipped. Installation
proceeds normally via the one-time boot, and after installation
`upi-forge eject` and a BIOS boot-order check are the line of defense against
an ISO reboot.

**failure at a middle node**
At the point of failure, a `--from <hostname>` resume command is shown. The
options used in the original run (`--skip-iso-check`, etc.) are included in
the guidance.

**some nodes fail in a parallel run (`bmc.parallel`)**
The remaining nodes proceed to the end, and at the end the list of failed
nodes and the command to re-run only those nodes (e.g.
`upi-forge boot worker5 worker7`) are shown. Check the cause in the pathset's
`logs/<command>-<run-time>-<unique>/<hostname>.log`. It contains the entire
per-node output that is shown on screen in a sequential run (including detail
if you turned on `--debug`). In parallel mode, failures are scattered
regardless of order, so retry with NODE arguments rather than `--from`.

**boot-order warnings appear but it keeps going**
This is intended behavior. The boot-order representation differs by machine
and firmware, so even if this protective step fails, installation continues
via the one-time boot. After installation, `eject` and a boot-setting check
are the final line of defense against an infinite reboot.

**installation finished but the node boots back into the install screen**
The virtual media is still attached. Run `upi-forge eject`. For a VM, detach
the virtual CD/DVD in the hypervisor.

## After installation

**the node does not appear in the cluster**
CSR approval, the standard UPI procedure, is required.

```bash
oc get csr | grep Pending
oc adm certificate approve <csr-name>   # usually twice per node
```

**when you want to see more logs — `--debug`**
Turning on the global option `--debug` (or `logger.debug: true` in the
configuration) lets you see the commands the tool runs and the Redfish
requests.

- Every external command printed with its arguments right before it runs:
  `debug: run: oc adm release extract ...`,
  `debug: run: coreos-installer iso customize ...`
- Every Redfish request sent to the BMC: `debug: POST https://<address>/redfish/v1/...`,
  session create/delete completion
- Relay of the external command's stderr: `debug: <command> stderr: ...`

```bash
upi-forge --debug boot
```

When behavior differs from what you expected, you can use this log to check
which commands/requests were sent. Tokens, passwords, and Redfish request
bodies are not left in the log even with debug.
