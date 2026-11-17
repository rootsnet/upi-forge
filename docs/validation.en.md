# Pre-flight Validation

[한국어](validation.md) | **English**

UPI Forge performs several validations before running to prevent accidents
like "booting a node of a different cluster" or "installing with the
certificate of a cluster from before a rebuild." This document explains
**what each validation checks and why**, how to respond when it fails, and
how far it can be skipped.

## At a glance

| Validation | When | Skip |
|---|---|---|
| hostname domain match | ignition / iso / boot | not possible |
| DNS forward/reverse registration | ignition | `--skip-dns-check` |
| cluster pre-flight check (oc login target) | prepare (when cluster.version is unset) / ignition | not possible |
| artifact provenance — static check | iso | not possible |
| artifact provenance — MCS certificate comparison | iso | `--skip-ca-check` |
| ISO URL reachability check | boot / live-boot | `--skip-iso-check` |

## 1. hostname domain match (ignition · iso · boot)

If a hostname in nodes.csv is an FQDN, its domain must match
`cluster.domain`.

```text
error: node hostname validation failed (1 item):
selected pathset: baremetal-example
file read: /opt/upi-forge/configs/pathsets/baremetal-example/nodes.csv
the node hostname's domain differs from the cluster domain: worker4.other.example.com
(cluster.domain: mycluster.example.com) — this may be a nodes.csv for a
different cluster, or the cluster.domain setting may be wrong
```

**Why**: if a nodes.csv for a different cluster is left behind, that node is
actually registered in its own cluster's DNS and so even passes the DNS
validation. Left as is, it produces an artifact that joins the wrong cluster.

**Response**: look at the pathset and file path shown in the error, and check
whether the wrong pathset was selected (`--pathset`/`pathsets.active`) or the
CSV contents are stale, and correct it. Short hostnames (names without dots)
are not subject to this check. Even if you override `cluster.apiURL`, that
host must belong to `cluster.domain`.

## 2. DNS registration validation (ignition)

If the hostname is an FQDN it is used as-is; if it is a short name it is
queried as `hostname.<cluster.domain>`, for both forward and reverse.

- **Forward (A)**: the query result must contain the IP from nodes.csv. It
  fails if there is no record, only a different IP is returned, or the server
  responds abnormally (SERVFAIL, etc.).
- **Reverse (PTR)**: it fails **only when an incorrect PTR is actually
  resolved**. A missing PTR (and every case that cannot be confirmed) passes
  with a warning — a settled policy so as not to block environments that keep
  no reverse zone.

Queries are sent **directly to the pathset's `network.dns` servers**, not the
system resolver. The goal is to confirm the registration state on the servers
the node will actually use, so it is unaffected by the bastion's `/etc/hosts`
or search domains. If there are multiple servers, it checks **every server
that responds**, and if the servers disagree it is an error (since the node
can use any of them).

**Response**: register/fix the A record on the DNS server and run again. It
collects the failures of all nodes and reports them at once, so you can fix
them in a batch. Use `--skip-dns-check` only in a validation environment
where DNS cannot be prepared yet. If `network.source: copy` and there is no
`network.dns`, it is skipped automatically with a warning.

## 3. Cluster pre-flight check (prepare · ignition)

It confirms that the current `oc` login target is the same as the cluster in
the configuration (`cluster.apiURL`) and that the cluster responds. When
working across multiple clusters, this catches the "config is A but login is
B" state.

**Response**: log in to the correct cluster with `oc login`, or change
`UPI_FORGE_CONFIG` to the correct configuration.

## 4. Artifact provenance validation (iso)

Before reusing an existing `<hostname>.ign`, it confirms that the artifact is
indeed **for this node and this cluster right now**.

**Static checks** (always performed, cannot be skipped):

- The `/etc/hostname` value inside the Ignition matches the target node
- The MCS address inside the Ignition matches the current configuration
- The structure matches this tool's artifact shape (detects tampering or
  manual edits)

**MCS certificate comparison**:

It fetches the certificate from the current cluster's MCS and compares it
against the certificate in the Ignition on a CA basis. **When a cluster is
rebuilt with the same domain**, the name and address are the same but the
certificate changes, so this comparison catches stale artifacts.

```text
error: the existing Ignition's MCS CA differs from the current cluster: ... —
the cluster may have been rebuilt. Delete the existing artifacts
(<hostname>.ign/.yaml/.iso) and re-run starting from `upi-forge ignition`
```

**Response**: as guided, delete that node's existing artifacts and regenerate
starting from `ignition`. In an environment where MCS cannot be reached at
the iso stage, you can skip **only the certificate comparison** with
`--skip-ca-check` (the static checks are still performed).

## 5. ISO URL reachability check (boot · live-boot)

Before booting, it reads the beginning of the published ISO URL with a GET
(Range) request to confirm it is reachable (HTTP 200/206 allowed). If the
check fails due to environment characteristics, skip it with
`--skip-iso-check` (whether iDRAC can actually fetch it is ultimately
determined at boot time).

## No overwriting

Separately from validation, existing artifacts are never overwritten.

- If `<hostname>.ign` or `<hostname>.iso` exists, it stops — to regenerate,
  delete it directly and run
- `<hostname>.yaml` (NMState) is reused **only when the contents are
  identical**; if different, it stops

An "already exists" error is not a defect but a rule to always make artifact
replacement an explicit operator action.
