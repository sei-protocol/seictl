# Validation Substrate: seictl as Test Orchestrator

**Status:** Draft (design)
**Scope:** Substrate for orchestrating validation tests against ephemeral Sei chains via seictl primitives + composites
**Authors:** Brandon Chatham (platform engineer); coral round dispatched platform-engineer / product-manager / product-engineer specialists for cross-review
**Last updated:** 2026-04-30
**Supersedes:** sei-protocol/sei-k8s-controller#143 (ValidationRun CRD LLD — merged 2026-04-28, abandoned 2026-04-30)
**Workload runtime contract dependency:** sei-protocol/platform#235 (kept verbatim)

---

## Summary

The original ValidationRun design (sei-protocol/sei-k8s-controller#143) modeled validation orchestration as a CRD reconciled by two cooperating sub-controllers in one binary, with a phase machine, condition machine, and per-controller plan persistence in `.status`. After implementation began, the value question surfaced: **what does putting this into a CRD actually buy?**

Honest answer: GitOps applicability + resilience to controller restart mid-run. Both are deliverable from a CLI substrate. Everything else the CRD shape was paying for (declarative desired state, edge-triggered reconciliation, status partitioning) is paying for semantics that test orchestration doesn't actually need — tests are imperative, time-bounded, and one-shot.

The substrate is the existing `seictl` binary, extended with a small set of resource primitives and composites:

| Layer | Verbs |
|---|---|
| **Composites (sugar)** | `bench up/down/list` (shipped), `qa up`, `shadow up` (deferred) |
| **Primitives** | `chain`, `rpc`, `load`, `harness`, `rules` — each with `up | down | list | wait | logs` (deferred) |
| **Distribution** | Single binary; standalone `seictl` AND kubectl plugin via `kubectl-sei` symlink |

**v1 ships effectively zero new code.** Today's `bench up` already covers the seiload-nightly use case (the LLD's primary Phase 1 consumer). Primitives land on demand with named triggers, not speculatively.

This document supersedes #143. The runtime workload contract from platform#235 (env vars, exit codes, S3 paths) is kept verbatim — it's already implemented in `bench up`.

## Goals

- **One tool that bridges resource provisioning + test orchestration** for engineers and agents. CLI for humans, MCP for agents, kubectl-plugin for engineers already in their kubectl flow.
- **Composable primitives over monolithic resources.** Engineers should be able to stand up a chain alone, peer an RPC fleet against it, fire a load harness — separately or composed.
- **Workload contract parity with Phase 1** (platform#235): a Job manifest that runs under autobake's bash glue runs unchanged under `seictl load up`.
- **GitOps- and ad-hoc-friendly**, without a CR. Engineers run commands directly; CI applies via `seictl ... -f manifest.yaml` (introduced only when a real consumer asks).
- **No cross-tenant blast radius.** Same engineer-namespace by construction (already enforced by IAM-aligned label scoping in shipped verbs).
- **MCP graduation is additive.** Each verb's JSON envelope is already the MCP tool-output shape; no separate translation layer.

## Non-goals (deliberate, from coral synthesis)

These are not "deferred to v1.1" — they are explicit anti-features that the abandoned LLD's gravitational pull will keep tempting us to build. Resist.

- **A unified `validation.sei.io/v1` YAML schema.** Each verb owns its inputs; composition lives in shell until a real consumer hand-rolls the same wrapper script twice.
- **A generic `harness` substrate** as a "configurable container Job runner." If qa-testing migrates from its bash glue, build `qa up` with qa-testing's *specific* contract, not a configurable substrate that pretends to be generic.
- **Symmetric verb sets for symmetry's sake.** `up | down | list | wait | logs` looks elegant in a table; in v1 most primitives ship with two or three verbs, not five. Add the rest when an engineer hits the gap.
- **Observability-as-test-oracle in the CLI.** The LLD wanted this because it was building a single-CR pass/fail. Engineers have Grafana and Alertmanager. Codify recurring failure-mode queries as saved Grafana panels first; promote to a `rules watch` Job only if the panel approach demonstrably fails.
- **Per-verb kubectl plugin symlinks** (`kubectl-bench`, `kubectl-onboard`, …). One `kubectl-sei` plugin preserves a single argv parser; per-verb symlinks would force an arg-rewriting fork.
- **Cross-primitive OwnerReferences.** Cascade-delete is label-driven (`sei.io/chain-id` selector). Adding OwnerRefs across primitives creates hidden coupling and conflicts with the SND controller's children semantics.
- **Test orchestration as a controller / reconcile loop.** The whole point of this design.

## Design

### Primitive surface

Five resource primitives, each independently `up`/`down`-able:

| Primitive | Provisions | Lifetime |
|---|---|---|
| `chain` | One SeiNodeDeployment of validators (genesis ceremony, peer mesh, chain-id) | long-ish — can outlive any single test |
| `rpc` | One SND of full-nodes peering with a named chain | tied to chain or shorter |
| `load` | A seiload Job firing traffic at chain/RPC endpoints, duration-bounded | ephemeral |
| `harness` | An arbitrary container Job (qa-testing, fuzzer, integration suite) targeting endpoints | ephemeral |
| `rules` | Prometheus-watcher Job evaluating alert/query rules over a window, writing verdict | tied to test window |

Each primitive's full verb surface — when materialized — is `up | down | list | wait | logs`. v1 ships subsets per primitive on demand.

**Today's `bench up` is a composite over chain + rpc + load.** It stays as the headline command and the canonical 80%-case path. The internal implementation can be refactored later to call into shared `runChainUp` / `runRPCUp` / `runLoadUp` Go functions; that refactor is not v1.

### Label & ownership contract

The unit of identity is `(namespace, chain-id)`. Every object any primitive applies carries:

| Label | Value | Purpose |
|---|---|---|
| `app.kubernetes.io/managed-by` | `seictl` | Tooling discrimination |
| `app.kubernetes.io/part-of` | `seictl` | Cluster-wide owner-scope |
| `app.kubernetes.io/component` | `chain` \| `rpc` \| `load` \| `harness` \| `rules` | Primitive selector |
| `sei.io/engineer` | `<alias>` | IAM-aligned ownership; matches namespace `eng-<alias>` |
| `sei.io/chain-id` | `<chain-id>` | Foreign key across primitives |
| `sei.io/role` | `validator` \| `fullNode` (chain/rpc only) | SND-role disambiguation |

**Cascade-delete is label-driven.** `chain down --chain X` deletes the union across primitive resource kinds where `sei.io/chain-id=X`. Default refuses if rpc/load/rules still reference the chain (prints what's attached); `--cascade` opts in to nuke everything. Composites like `bench down` cascade by definition.

**Cross-primitive OwnerReferences are explicitly NOT used.** Tempting but rejected: SND controllers don't claim ownership over arbitrary children, and cross-primitive ownership creates hidden coupling that defeats independent primitives.

**OwnerReferences within a primitive are fine** (e.g., a load Job owns its rendered ConfigMap).

**Field manager:** one `seictl-bench` is shipped today. Per-primitive split (`seictl-chain`, `seictl-rpc`, …) is deferred until an SSA conflict actually fires in real use. One-way door — change this later carefully; renaming abandons SSA ownership of every previously-applied object.

### Chain ↔ RPC peer discovery

The SND CRD already has `peers[].label.selector` (see `LabelPeerSource` in sei-k8s-controller's `api/v1alpha1/common_types.go`). The substrate uses it directly — no new mechanism, no new flag.

When `rpc up --chain <chain-id>` runs, it renders an SND with:

```yaml
spec:
  template:
    spec:
      peers:
        - label:
            selector:
              sei.io/chain-id: <chain-id>
              sei.io/role: validator
            namespace: <chain-ns>
```

The SND controller resolves the selector to headless Service DNS on every reconcile, so the rpc fleet picks up validators as they come up. No explicit ordering required between `chain up` and `rpc up`.

Genesis is the gating coupling, not peers. The validator SND uploads to a chain-id-derived S3 bucket convention; `rpc up` renders the SND with `spec.template.spec.snapshot.s3` pointing at the same convention. Validator-SND-controller already implements this; the substrate depends on it.

### `wait` semantics

Each primitive has a typed terminal predicate:

| Primitive | Terminal condition |
|---|---|
| `chain` / `rpc` | SND `status.phase=Ready` AND `readyReplicas=spec.replicas` |
| `load` / `harness` | Job `status.conditions[Complete]=True` (succeeded) or `status.conditions[Failed]=True` |
| `rules` | Watcher Job terminal (verdict on `/dev/termination-log`) |

`seictl <primitive> wait` is informer-backed (typed watch on the resource kind), exits on terminal. Default timeout per primitive: chain 20m, rpc 10m, load `duration+5m`, rules `duration+1m`. Override via `--timeout`.

Composites (`bench up`) compose primitive waits: `chain wait → rpc wait → load wait`.

**Foreground default.** `up` blocks until ready/terminal by default — engineers in a terminal expect blocking. `--no-wait` flips to fire-and-exit; agents and CI use this and call `wait` separately when they want to block.

**stderr is the progress channel; stdout is the JSON envelope.** Streamed phase-transition lines go to stderr (e.g., `{"primitive":"chain","phase":"Initializing","ready":3,"desired":4}`); the typed result envelope still lands on stdout at the end. Engineers tail stderr and pipe stdout to `jq`.

### `rules watch` — a Job, not a controller

The CRD LLD designed an in-process polling loop (`monitor-task-completion`) that watched the load Job + Prometheus + emitted verdict to status. Translating to "a Job in the cluster":

- **Container**: a small Go binary (`seictl-rules-watcher`, ~150 LoC), built from this same repo, image-pinned.
- **Inputs**: rule spec via mounted ConfigMap (schema = the LLD's `[]ValidationRule` types, copied into `cluster/internal/rules/`); Prometheus URL via env (default `http://prometheus-k8s.monitoring.svc:9090`); duration via env.
- **Output**: `/dev/termination-log` for one-line verdict JSON; S3 verdict.json under `s3://harbor-validation-results/<namespace>/rules/<chain-id>/<run>/`; exit code 0/1/2 per platform#235's contract.
- **Lifecycle**: `activeDeadlineSeconds = duration + 60s`. `ttlSecondsAfterFinished: 3600`.
- **Stop-on-failure cross-primitive coordination**: deferred. v1 records verdicts; the composite (e.g. `bench up`) sees the rules Job's exit code via `wait` and decides whether to kill the load Job. Single-actor coordination, no in-cluster cross-Job IPC.

**Un-defer trigger:** an engineer files an issue like "my bench passed but validators were OOM-ing the whole window" — that's the painkiller signal. Until then, engineers eyeball Grafana.

### Composite implementation

Composites are **in-process Go**, not shell-out:

- Each primitive exports a `runChainUp(ctx, in, deps) (Result, error)` — same DI pattern as today's `runBenchUp`.
- Composites import these directly. `bench up` = `runChainUp → runChainWait → runRPCUp → runRPCWait → runLoadUp → runLoadWait`.
- Why not shell-out: typed `*clioutput.Error` carries an exit-code category (lost across exec); MCP server would self-fork; testability requires fake binaries on PATH (gross).

The composite envelope embeds the primitive envelopes:

```go
type BenchUpResult struct {
    Chain ChainUpResult `json:"chain"`
    RPC   RPCUpResult   `json:"rpc"`
    Load  LoadUpResult  `json:"load"`
    // ... existing aggregate fields (chainId, namespace, resultsS3Uri, etc.)
}
```

JSON consumers can walk into per-primitive details; non-JSON readers see the headline fields they already use.

### Distribution

Single binary. Two install paths:

```sh
# Standalone (already shipped):
go install github.com/sei-protocol/seictl@latest

# kubectl plugin — one symlink, no code change:
sudo ln -s "$(which seictl)" /usr/local/bin/kubectl-sei
```

Both invocations route through the same argv parser (cli/v3 reads `os.Args[1:]`):

- `seictl bench up --image X` (standalone)
- `kubectl sei bench up --image X` (plugin)

The `sei` namespace fits kubectl-plugin convention (`kubectl krew`, `kubectl tree`, `kubectl ns`, `kubectl debug` — short, no `-cli`/`-ctl` suffix). It also implicitly claims the kubectl plugin namespace for the Sei platform — fine, since `seictl` is the canonical tool.

Per-verb plugin symlinks (`kubectl-bench`, `kubectl-onboard`) are explicitly rejected: they'd require argv rewriting (kubectl strips just the verb prefix when invoking a plugin), bloat `kubectl plugin list`, and fork the parser.

### MCP graduation

Each composite becomes a first-class MCP tool: `bench_up`, `shadow_up`, `qa_up`, etc. Their tool descriptions cover ~95% of agent traffic.

Primitives are exposed as a single decompose-shaped escape hatch:

```
seictl_primitive(primitive: "chain"|"rpc"|"load"|"harness"|"rules",
                 verb: "up"|"down"|"list"|"wait"|"logs",
                 args: { ... })
```

Agents reach for this only when the composite docs say "for custom topology, decompose with `seictl_primitive`." Read-only verbs (`*_list`, `*_logs`) and a unified `seictl_wait(handle)` register as their own tools — they're cheap to misuse.

The JSON envelope is already MCP-output-shaped (`apiVersion: seictl.sei.io/v1` + `Kind` + `Data` + `Error`). Each verb keeps its own `Kind`; structured-output unions discriminate on it.

### v1 ship cut

| Artifact | v1 | Trigger to un-defer |
|---|---|---|
| `bench up`, `bench down`, `bench list` | **Shipped** | — |
| `bench wait`, `bench logs` | **Defer** | Engineers run `bench list` in shell loops as workaround |
| `chain up/down/list/wait/logs` standalone | **Defer** | A second composite (`qa up` / `shadow up`) gets approved AND a real engineer wants a chain without a load attached |
| `rpc up/down/list/wait/logs` standalone | **Defer** | First request to peer an RPC fleet against an existing chain (mid-run refresh, shadow replay) |
| `load up/down` standalone | **Defer** | Engineer needs to re-run load against an in-flight bench without re-rolling validators |
| `harness up/down` | **Defer** | qa-testing consumer commits to migrate from bash glue, with their specific contract |
| `rules watch` | **Defer** | First "passed-but-validators-OOM" issue filed |
| `qa up`, `shadow up` composites | **Defer** | Same trigger as their underlying primitives |
| `scenario run -f x.yaml` | **Defer hard** | Three composites exist and want the same orchestration shape |
| `validation.sei.io/v1` YAML schema | **Defer** | An engineer hand-rolls the same wrapper script twice |
| kubectl plugin install (one-line symlink) | **Document in v1** | — (zero code change) |
| Per-primitive field manager split | **Defer** | An SSA conflict actually fires |

**Net new code in v1:** kubectl plugin install instructions + JSON envelope embedding (when composites refactor under shared primitives, but that's not gating). The rest lands on demand.

## Alternatives considered

### A. Continue with the ValidationRun CRD (#143's path)

**Rejected.** Two cooperating reconcilers + phase machine + condition machine + per-controller plan persistence + admission webhooks + CRD versioning lifecycle is paying meaningful complexity for two real wins (GitOps, mid-run controller-restart resilience). Both are deliverable from CLI:

- GitOps: `seictl ... -f manifest.yaml` against the same YAML shape. CI/Flux applies; CLI materializes. Same story.
- Resilience: bake long-running polling into Jobs (`rules watch` is one). K8s restarts the Job on crash; no controller needed.

The complexity savings are large. The LLD itself was 1588 lines; this design replaces it with a fraction of that.

### B. Unified `validation.sei.io/v1` YAML schema as universal CLI input

**Rejected (deferred without trigger).** The CRD bundled three independent contracts (workload runtime, Prometheus rules, chain spec) because a CRD is a single resource. The CLI doesn't have that constraint. Each verb owns its own input shape — flag-driven for the common case, optional small YAML when needed. Forcing unification optimizes for consumers that don't exist yet.

### C. Per-verb kubectl plugin symlinks (`kubectl-bench`, etc.)

**Rejected.** `kubectl bench up` would invoke the plugin with just `up` (kubectl strips the verb prefix), forcing argv-rewriting logic and N symlinks per top-level verb. `kubectl-sei` (single prefix) keeps one parser, one help tree.

### D. seictl-as-shell-out from composites

**Rejected.** Composites (`bench up`) calling primitives via `os/exec` would lose typed errors across the boundary, fork the MCP server back into itself, and hurt testability (fake binaries on PATH). In-process Go function calls preserve all three.

### E. `rules watch` as a sidecar inside the load Job (vs. its own Job)

**Considered.** A sidecar would simplify lifecycle (one Job, two containers). Rejected for v1 because it forces every load workload to know about Prometheus, and breaks for opaque third-party harnesses (qa-testing). Standalone Job is the more decoupled shape. Reconsider if a real consumer wants the sidecar form.

### F. Argo Workflows / Tekton

**Considered (already during the LLD's coral round, unanimously rejected then).** External workflow engines can't natively own non-Pod CRDs (e.g., SeiNodeDeployment) cascade-style. The substrate-here approach owns SNDs by writing them with consistent labels and cascading by selector — the workflow-engine concern doesn't apply.

## Trade-offs

What we lose vs. the CRD path, and why we accept it:

- **No `kubectl get validationruns` view.** Mitigated by label-selected aggregation: `kubectl get all -l sei.io/chain-id=X` shows the whole stack; `seictl bench list` aggregates by labels.
- **No GitOps "this validation should always be running" semantics.** Mitigated by: tests are inherently one-shot, not desired-state. For recurring validations, a `CronJob` running `seictl bench up --apply` is three lines of YAML.
- **No automatic retry on transient failure.** Mitigated by: tests are short, engineers re-run; this is not production traffic.
- **No status-aggregation across many runs in `kubectl get`.** Mitigated by: S3 results bucket is the durable history; query it directly.
- **Lose mid-run controller-restart resilience.** Mitigated by: long-running pieces become Jobs (rules watcher, future workload monitoring), which K8s restarts. The CLI invocation is short-lived by design.

What we gain:

- ~1100 fewer lines of design + maintenance per "actor type" we'd add to the CRD model.
- Same binary serves three audiences (CLI, kubectl-plugin, MCP) with one parser.
- Each verb is independently testable, independently deployable.
- No CRD lifecycle (versioning, conversion, deprecation) to maintain.
- Engineers compose flexibly with shell, scripts, GHA, agents — no controller-mediated abstraction in the way.

## Open questions

1. **`seictl-rules-watcher` image distribution.** Built from this repo, published to which ECR? Reuse `189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/`? Owned image policy parity with seiload.
2. **`harness` env var contract.** When qa-testing migrates, which envs are workload-specific vs platform-shared? The LLD reserved `${RESULT_DIR}` and a few others; a real consumer pins this.
3. **Composite refactor priority.** When does `bench up` get refactored into shared `runChainUp` / `runRPCUp` / `runLoadUp` Go functions? Punt until a second composite (`shadow` or `qa`) actually wants them.
4. **JSON envelope embedding shape.** When composites embed primitive results, do we keep the existing flat `BenchUpResult` (current shape) for backwards-compat *and* add nested fields, or break compat at v2? Lean: keep both during a transition.
5. **kubectl plugin discovery on engineer laptops.** Krew distribution, brew formula, just `go install` instructions? Pick one for v1 docs.

## References

- **sei-protocol/sei-k8s-controller#143** — abandoned ValidationRun CRD LLD (merged 2026-04-28). Read for the problem statement, OSS survey, and 18 resolved one-way-door decisions, several of which (e.g., the discriminator union) become moot here.
- **sei-protocol/platform#235** — workload runtime contract (envs, exit codes, `${RESULT_DIR}`, S3 path convention). Kept verbatim. Already implemented in `seictl bench up`.
- **`docs/design/cluster-cli.md`** — the shipped v1 LLD covering `context`, `bench`, `onboard`. This document extends its non-goals (rules watch, harness, scenario YAML) and adds the substrate framing without contradicting the verb surface it documents.
- **sei-protocol/sei-k8s-controller `api/v1alpha1/common_types.go`** — `LabelPeerSource` is the SND-controller mechanism the chain↔RPC peer discovery rides.
- **autobake's existing pattern** — production proof that imperative-orchestrator-of-K8s-resources is the right shape for ephemeral test workloads.
- Coral round (2026-04-30): platform-engineer (substrate), product-manager (scope discipline), product-engineer (cross-surface ergonomics). Outputs synthesized inline above.
