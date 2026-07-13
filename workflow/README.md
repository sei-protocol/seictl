# State-Sync Workflow

`seictl workflow state-sync` re-bootstraps a SeiNode through CometBFT state sync. It renders the StateSync recipe against a target node, server-side-applies the resulting `SeiNodeTaskWorkflow`, and streams the plan's progress until the workflow reaches a terminal phase. The recipe holds the node's readiness gate, stops seid, wipes the data directory, reconfigures state sync against the witnesses, releases the gate so seid restarts, and waits for the node to begin catching up.

There are two ways to run it. A plain resync clears the node's data and lets it re-catch-up on its existing config. A store migration does the same wipe-and-resync but also changes seid's storage configuration on the way. The plain resync is the common case and takes no extra flags; the migration is a deliberate, destructive operation behind its own flags.

## Plain Resync

No migration flags. This clears the data directory and re-bootstraps the node from peer snapshot data on its existing config:

```
seictl workflow state-sync <node>
```

The node holds, wipes, reconfigures, restarts, and begins catching up. Its config is unchanged.

### Plain Resync Steps

seictl renders the workflow and applies it to the cluster. If a workflow with the same name already finished or failed, the command refuses and explains what to do. Once the node has no other work in flight, the controller picks up the workflow, records the plan, and runs six steps in order:

1. **mark-not-ready** starts the hold. The sidecar marks the node not ready, so seid cannot start again until the workflow allows it. It also clears any earlier mark-ready records so nothing can release the node by accident.
2. **stop-seid** stops the node process. seid gets a graceful shutdown signal and the task confirms it exited. Kubernetes restarts the container, and the restart waits at the hold instead of starting seid.
3. **reset-data** clears the chain data. Only the `data/` directory is wiped. The node's identity keys, its config files, and the sidecar's task history all stay in place, and a fresh empty `priv_validator_state` file is written back.
4. **configure-state-sync** sets up the resync. The sidecar asks the witnesses for a trusted block height and hash and writes them into the node's config. The two-witness minimum is enforced earlier, when the plan is built, so a node is never wiped for an under-witnessed config (see Witnesses).
5. **mark-ready** releases the hold. seid starts on the empty data directory, downloads a recent snapshot from its peers, and checks it against the witnesses' trusted block.
6. **await-condition** waits for the node to catch up to the head of the chain.

When the last step finishes the workflow is Complete, the node goes back to normal operation, and seictl exits 0.

## Witnesses

State sync verifies the snapshot it restores against a trusted header. `--rpc-servers` sets the CometBFT light-client servers (a primary plus witnesses) used for that verification. The flag is optional; when omitted, the node's resolved state-syncers are used. When you set it, the servers are bare `host:port`, repeatable, and at least two are required or the plan refuses to compile.

```
seictl workflow state-sync <node> --rpc-servers host-a:26657 --rpc-servers host-b:26657
```

These servers are for trust-point verification only. Snapshot chunks arrive separately over p2p from snapshot-serving peers, so a witness is not a snapshot provider.

## Store Migration

A migration runs a named seid config change inside the resync. Today the one supported migration is the giga SS store split:

```
seictl workflow state-sync <node> --migration GigaStore --backend pebbledb
```

This is destructive and slow, and it is not reversible without another resync. It discards the node's local state and re-bootstraps on the chosen backend. Both tokens are required, so a migration cannot be triggered by a single flag. `--backend` is `pebbledb` or `rocksdb`, where rocksdb needs a seid image built with `-tags rocksdbBackend`. The migration sets the giga flags itself (`ss-enable`, `evm-ss-split`, `ss-backend`, and `sc-enable`); the backend value is the only operator input.

When `--migration` is set, the command prints a `seictl:` destructive-migration warning to stderr before it applies anything. Run `--dry-run` first to see the rendered workflow, including the migration, without persisting it.

### Migration Steps

A migration follows the same steps with one addition. The migration's config changes are computed and checked when the plan is built, before anything runs, so an invalid migration fails immediately and the node is untouched. The plan also records exactly which config keys will change, so you can read what a migration did after the fact.

1. **mark-not-ready**, **stop-seid**, and **reset-data** run exactly as in a plain resync. The node is held, seid stops, and the chain data is cleared while identity and config stay in place.
2. **config-patch** applies the migration's config changes. For the giga store split this sets `ss-enable`, `evm-ss-split`, and `ss-backend` under `[state-store]` in `app.toml`, and `sc-enable` under `[state-commit]`.
3. **configure-state-sync** sets up the resync from the witnesses, as in a plain resync.
4. **mark-ready** releases the hold. seid starts under the new storage layout for the first time, and the snapshot restore fills the new store as it downloads.
5. **await-condition** waits for catch-up. seid refuses to start if the new store layout is empty while the split is enabled, so a node that catches up and serves EVM requests is solid evidence the migration worked.

If a migration fails partway, the node stays held with its data already cleared. seid stays down and the sidecar stays reachable for diagnosis. Recovery is another resync. Force-delete the failed workflow and run a new one, with the same migration or without it.

## Plain Versus Migration

| | Plain resync | Store migration |
|---|---|---|
| Flags | none | `--migration GigaStore --backend <backend>` |
| Config | unchanged | giga SS store flags set |
| Effect | re-catch-up on existing config | wipe, switch storage engine, re-catch-up |
| Destructive | yes, data cleared then re-synced | yes, data cleared, engine changed, not reversible without another resync |

Both paths wipe and re-sync. The migration additionally changes the storage engine. Omitting the migration flags is always the plain path.

## Recipe Mapping

The flags map onto the `SeiNodeTaskWorkflow` spec:

- `<node>` sets `spec.target.nodeRef.name`
- `--migration GigaStore` sets `spec.stateSync.migration.kind`
- `--backend <backend>` sets `spec.stateSync.migration.gigaStore.backend`
- `--rpc-servers` sets `spec.stateSync.rpcServers`

`--dry-run` emits this CR so you can read it back before applying. The controller compiles the spec into the ordered task plan (mark-not-ready, stop-seid, reset-data, the migration's config-patch step when present, configure-state-sync, mark-ready, await catching-up) and drives it.

## Re-Run Semantics

Spec params are immutable, so re-running over a same-named workflow already in a terminal phase is refused. A Failed workflow holds the node, leaving it not-ready until the workflow is removed, so recovering a failure is the operationally important case. The three recovery paths:

- A Complete run: delete it with `seictl workflow delete <name>`, then re-run.
- A Failed run: force-delete it by setting the annotation `sei.io/force-delete-workflow=<reason>`, then re-run.
- Either case: pass `--name` to run under a fresh workflow name instead.

## Output And Exit Codes

The command is built for scripts and agents:

- The exit code is kubectl-wait-compatible: 0 when `.status.phase` reaches Complete, and nonzero on a Failed phase, a `--timeout`, or an apply or watch error.
- stdout is NDJSON. In watch mode each line is the full workflow object as observed, re-emitted on every change, so a caller reads `.status.phase` for the terminal transition and `.status.plan.tasks` for per-step progress. Under `--dry-run` or `--no-watch` stdout is instead the single rendered CR.
- stderr carries two kinds of output. Progress and diagnostic lines are human-readable and prefixed `seictl:` (the target line, the migration warning, the watch line). On failure the last thing written to stderr is a single `metav1.Status` JSON object.
- To read the failure cause programmatically, capture stderr separately from the NDJSON stdout, then strip the `seictl:` lines and parse the Status, for example `seictl workflow state-sync <node> 2>err.log; grep -v '^seictl:' err.log | jq -r .reason`. Keeping stderr off stdout leaves the NDJSON event stream clean. The `.reason` is `Timeout` for a `--timeout`, `InternalError` for a terminal Failed phase, and the apiserver's own reason for an API error.
- `--timeout` bounds the watch. A full state sync can be slow, so size it for the target's dataset.

See `seictl workflow state-sync --help` for the full flag list.

## Migrating Off Config-Patch

`--config-patch` is not a valid flag; passing it is an error that names the replacement. Config patching is a typed migration. For the giga store split use `--migration GigaStore --backend <pebbledb|rocksdb>`. A plain resync takes no migration flag.
