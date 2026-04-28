# Cluster CLI: Low-Level Design

**Status:** Draft (design)
**Scope:** New cluster-facing subcommands for `seictl` — `bench`, `onboard`, `context`
**Authors:** sei-platform-engineer skill council (platform-engineer lead, kubernetes-specialist, security-specialist)
**Last updated:** 2026-04-27 (v2 — MVP-trimmed per maintainer review)
**Tracking:** sei-protocol/Tide#8 (sei-platform-engineer skill draft, depends on this)

---

## Summary

`seictl` gains a small family of cluster-facing commands so engineers can provision benchmark workloads on the harbor EKS cluster without touching kubectl, Kustomize, or Terraform. v1 ships **five verbs**:

| Verb | Purpose |
|---|---|
| `seictl context` | Cluster + identity ground truth |
| `seictl onboard` | Set up engineer environment (namespace files via PR + IAM via AWS SDK) |
| `seictl bench up` | Render and apply benchmark workloads (validators + RPC + seiload Job) |
| `seictl bench down` | Tear down a benchmark by name |
| `seictl bench list` | Owner-scoped list of running benchmarks |

The conversational layer is the `sei-platform-engineer` skill at sei-protocol/Tide#8. This LLD is the implementation contract that skill depends on.

## Goals

- Engineer types one command (eventually one natural-language sentence to a Claude skill); benchmark runs.
- JSON output schemas are the **MCP tool contract** for v2 graduation — schemas defined here are stable.
- Reuse harbor's existing patterns (Pod Identity, embedded templates derived from autobake source) — no new auth surface, no template fork.

## Non-goals (deferred from v1, deliberate)

- `seictl seinode list / diagnose` — engineers can `kubectl get seinode` and `kubectl logs` directly. Codify recurring failure patterns in v1.1 once we know what they are.
- `seictl controller inspect` — `kubectl get pods -n sei-system` and `kubectl get lease` cover this.
- `seictl status` — `bench list` covers benchmark visibility; cluster-wide ownership view is v1.1.
- Production-context refusal logic — engineers don't have prod kubeconfig contexts; the auth boundary enforces the separation. No CLI-level redundancy.
- Error-redaction middleware — standard `cli.Exit` output is fine; add real redaction if a real leak surfaces.
- Loki/Grafana log integration — out of scope for any v1 verb.
- MCP server — v2. JSON schemas in this LLD prepare for it.
- Multi-cluster support — harbor-only. `--context` flag wired but exercised against one cluster only.
- Auto-cleanup of expired benchmarks — engineer triggers `bench down` or revert PR.
- Per-engineer ECR namespace for image push — engineers test ECR-resident images only in v1.
- Auto-merge of onboarding PRs — engineer manually requests review in v1.
- Automated template re-sync from upstream — manual seictl PR per autobake template change.

## Dependencies

- New Go module deps:
  - `sigs.k8s.io/controller-runtime/pkg/client` (typed and Unstructured CRUD)
  - `k8s.io/cli-runtime/pkg/genericclioptions` (kubeconfig + kubectl-plugin idiomatic)
  - `k8s.io/client-go/kubernetes` (already transitive — `SelfSubjectAccessReview`-free in v1)
  - `k8s.io/apimachinery/pkg/apis/meta/v1/unstructured` (CR access without typed package)
  - `github.com/google/go-containerregistry/pkg/name` (image ref parsing)
  - AWS SDK v2: `iam`, `eks`, `ecr` clients
  - `github.com/sei-protocol/seilog` — structured logging (matches existing seictl convention; logs to stderr, stdout reserved for JSON envelope)
- **Not** depending on `sei-k8s-controller/api/v1alpha1` in v1 — the package needs structural fixes (circular dependency risk) before seictl can safely import it. Tracked separately by sei-k8s-controller maintainer; revisit once landed.
- External engineer-side: `gh` authenticated, kubeconfig with `harbor` context.
- Upstream coordination: `sei-protocol/platform autobake/templates/*` and `autobake/profiles/autobake_evm_transfer.json` — embedded into `templates/` with provenance comment in each file.

---

## Architecture

### Package layout

```
seictl/
  main.go                    [edit: register new top-level commands]
  bench.go                   [new]
  onboard.go                 [new]
  context.go                 [new]
  internal/
    kube/                    [new — client construction, kubeconfig resolution]
    render/                  [new — text/template renderer over embed.FS]
    aws/                     [new — ECR digest resolver, IAM + Pod Identity helpers]
    identity/                [new — engineer.json read/write, validation]
    validate/                [new — input validation regexes]
    clioutput/               [new — Envelope, ErrorBody, exit-code mapping]
  templates/                 [new embed.FS root]
    embed.go                 [//go:embed * → fs.FS]
    snd-validator.yaml.tmpl  [provenance comment header points at autobake source]
    snd-rpc.yaml.tmpl
    seiload-job.yaml.tmpl
    autobake_evm_transfer.json.tmpl
    namespace.yaml.tmpl
    bench-seiload-sa.yaml.tmpl
    kustomization.yaml.tmpl
  Makefile                   [edit: lint-strict]
  docs/design/cluster-cli.md [this file]
```

**Why flat at repo root for top-level command files:** seictl's existing convention is one file per top-level verb (`config.go`, `genesis.go`, `serve.go`, `await.go`). New verbs preserve that. Internal packages handle implementation depth.

### Common flags

Defined once in `internal/clioutput`, inherited by every cluster-facing command. `genericclioptions.ConfigFlags` wires `--kubeconfig`, `--context`, `--namespace`/`-n` once at the root command.

```go
&cli.StringFlag{Name: "format", Value: "json", Usage: "Output format: json|text"}
```

### Output envelope

```go
type Envelope struct {
    Kind    string          `json:"kind"`              // e.g. "bench.up.result"
    Version string          `json:"version"`           // schema version, "v1"
    Data    json.RawMessage `json:"data,omitempty"`
    Error   *ErrorBody      `json:"error,omitempty"`
}

type ErrorBody struct {
    Code     int    `json:"code"`     // exit code
    Category string `json:"category"` // stable enum
    Message  string `json:"message"`  // one-line summary
    Detail   string `json:"detail,omitempty"`
}
```

No partial-state tracking. On any failure mid-command, exit non-zero with `category` + `message`. Recovery is re-running the command — `bench up` is idempotent via SSA, `bench down` is idempotent via label-selected delete.

---

## Subcommand reference

### `seictl context`

No required flags. Side-effect-free reads only.

```go
type ContextResult struct {
    KubeContext     string    `json:"kubeContext"`
    Cluster         string    `json:"cluster"`         // derived from server URL or kubeconfig context name
    Server          string    `json:"server"`
    Namespace       string    `json:"namespace"`
    AWSAccount      string    `json:"awsAccount"`
    AWSRegion       string    `json:"awsRegion"`
    AWSPrincipalARN string    `json:"awsPrincipalArn"`
    Engineer        *Engineer `json:"engineer,omitempty"` // nil if engineer.json missing
}

type Engineer struct{ Alias, Name string }
```

Six fields. The Claude skill surfaces this to the engineer at the start of a session so they can confirm where they are and who they are.

### `seictl onboard`

```
seictl onboard --alias <alias> [--name <name>]
               [--platform-repo <path>] [--no-pr] [--apply]
```

With `--apply`, performs both side effects:

1. Generates `clusters/harbor/engineers/<alias>/{kustomization,namespace,bench-seiload-sa}.yaml` in the platform repo, branches `<alias>/onboard-<alias>`, opens a PR via `gh`.
2. Creates the IAM policy + Pod Identity association directly via AWS SDK in the engineer's SSO session — `iam:CreatePolicy`, `iam:CreateRole`, `iam:AttachRolePolicy`, `eks:CreatePodIdentityAssociation`. No Terraform.

Without `--apply`: dry-run; prints what would be created.

```go
type OnboardResult struct {
    Alias          string         `json:"alias"`
    IdentityPath   string         `json:"identityPath"`
    GeneratedFiles []string       `json:"generatedFiles"`     // platform-repo paths
    Branch         string         `json:"branch,omitempty"`
    PRURL          string         `json:"prUrl,omitempty"`
    AWSResources   []AWSResource  `json:"awsResources"`       // {kind, arn, action: "create"|"exists"|"would-create"}
    DryRun         bool           `json:"dryRun"`
}

type AWSResource struct{ Kind, ARN, Action string }
```

**No `--principal-arn` flag.** Engineer's IAM principal is derived from `aws sts get-caller-identity` and cross-checked against the alias regex.

### `seictl bench up`

```
seictl bench up --image <ref> --name <name>
                [--size s|m|l] [--duration <duration>] [--apply]
```

Required: `--image`, `--name`. Defaults: size `s`, duration `30m`, namespace = `eng-<alias>` from identity.

Default behavior is dry-run. `--apply` performs server-side apply.

```go
type BenchUpResult struct {
    ChainID            string        `json:"chainId"`         // "bench-<alias>-<name>"
    Name               string        `json:"name"`
    Namespace          string        `json:"namespace"`
    ImageRef           string        `json:"imageRef"`        // input ref (tag or digest)
    ImageDigest        string        `json:"imageDigest"`     // resolved sha256:...
    Size               string        `json:"size"`
    Validators         int           `json:"validators"`
    RPCNodes           int           `json:"rpcNodes"`
    Duration           string        `json:"duration"`        // Go duration string
    ResultsS3URI       string        `json:"resultsS3Uri"`
    DryRun             bool          `json:"dryRun"`
    Manifests          []ManifestRef `json:"manifests"`
    AppliedAt          *time.Time    `json:"appliedAt,omitempty"`
}

type ManifestRef struct{ Kind, Name, Namespace, Action string } // action: "create"|"update"|"unchanged"
```

Sizes:

| Size | Validators | RPC |
|---|---|---|
| `s` | 4 | 1 |
| `m` | 10 | 2 |
| `l` | 21 | 4 |

Chain ID convention: `bench-<alias>-<name>` (engineer benchmarks); RPC SND is `bench-<alias>-<name>-rpc`. Distinguishes from autobake's nightly `autobake-<run-id>`.

S3 results URI: `s3://harbor-validation-results/<namespace>/<job>/<run>/report.log`. Shared bucket per the platform's validation-results schema; namespace prefix is the per-engineer IAM scope. For engineer benches the segments resolve to `eng-<alias>/evm-transfer/<name>/`.

### `seictl bench down`

```
seictl bench down --name <name> [--namespace <ns>] [--wait] [--timeout 5m]
```

Label-selected delete with `metav1.DeletePropagationForeground`. No dry-run flag — down is bounded and idempotent (re-run is fine if interrupted).

```go
type BenchDownResult struct {
    Name      string        `json:"name"`
    ChainID   string        `json:"chainId"`
    Namespace string        `json:"namespace"`
    Resources []ManifestRef `json:"resources"`     // action: "deleted" | "not-found" | "still-terminating"
    DeletedAt *time.Time    `json:"deletedAt,omitempty"`
}
```

On finalizer-stuck timeout: report still-terminating resources; exit non-zero with `category: "finalizer-stuck"`. Engineer uses `kubectl` independently if they want recovery — seictl doesn't redirect to other tools.

### `seictl bench list`

```
seictl bench list [--all-namespaces] [-n <namespace>]
```

Owner-scoped via labels (see [Label discipline](#label-discipline)).

```go
type BenchListResult struct {
    Items []BenchSummary `json:"items"`
}
type BenchSummary struct {
    ChainID           string `json:"chainId"`
    Name              string `json:"name"`
    Namespace         string `json:"namespace"`
    Owner             string `json:"owner"`
    Phase             string `json:"phase"`           // SND aggregate phase, read from `.status.phase` via Unstructured
    ValidatorsReady   int    `json:"validatorsReady"`
    ValidatorsDesired int    `json:"validatorsDesired"`
    RPCReady          int    `json:"rpcReady"`
    RPCDesired        int    `json:"rpcDesired"`
    LoadJobPhase      string `json:"loadJobPhase"`    // "Pending"|"Running"|"Succeeded"|"Failed"
    AgeSeconds        int64  `json:"ageSeconds"`
    ImageDigest       string `json:"imageDigest"`
}
```

---

## Kubernetes integration

### Client choice

- `sigs.k8s.io/controller-runtime/pkg/client` for typed access to standard objects (Service, ConfigMap, Job) and Unstructured access to SeiNode/SeiNodeDeployment CRs.
- `k8s.io/cli-runtime/pkg/genericclioptions` for kubeconfig + flag wiring.
- Wrap behind `internal/kube`:

```go
type Client struct {
    CR         client.Client
    RESTConfig *rest.Config
    Namespace  string
    Context    string
}
func New(flags *genericclioptions.ConfigFlags) (*Client, error)
```

Every command takes `*kube.Client`. No command pokes raw clientsets.

### CR access via Unstructured

Until `sei-k8s-controller/api/v1alpha1` is restructured for safe import, seictl uses Unstructured for SeiNode and SeiNodeDeployment CRUD:

```go
sndGVK := schema.GroupVersionKind{Group: "sei.io", Version: "v1alpha1", Kind: "SeiNodeDeployment"}
snd := &unstructured.Unstructured{}
snd.SetGroupVersionKind(sndGVK)
// Apply via SSA, list via label selector, etc.
```

Field access by string keys (`unstructured.NestedString(snd.Object, "status", "phase")`). Less ergonomic than typed access; acceptable for v1's narrow surface (status.phase, replica counts). When the controller package becomes safely importable, switch to typed access without changing the CLI surface.

### Apply strategy: server-side apply

`bench up --apply` uses server-side apply with field manager `seictl-bench` and **no force-ownership**:

```go
ri.Patch(ctx, obj.GetName(), types.ApplyPatchType, body, metav1.PatchOptions{
    FieldManager: "seictl-bench",
})
```

Why SSA:

- Idempotent re-runs of `bench up --name foo` — re-applying converges to the same state, not a Create-conflict scramble.
- Field-manager segregation: controller writes `status.*` with its own field manager; SSA naturally segregates so seictl never fights status writes.
- Conflict detection on unexpected writers — 409 with the conflicting field manager listed. With Force off, an outside writer (kubectl, a stale tool, a manager-name typo) surfaces loudly instead of being silently overwritten. If a legitimate ops-rescue path ever needs to take ownership, gate Force behind an explicit `--force-ownership` flag rather than defaulting it on.

`--apply` semantics:

- Default: render templates, print summary of resources that would be created/updated/unchanged, stop.
- `--apply`: perform SSA patches.

### Label discipline

Every resource seictl creates carries:

| Label | Value | Set by |
|---|---|---|
| `app.kubernetes.io/managed-by` | `seictl` | always |
| `app.kubernetes.io/part-of` | `seictl-bench` or `seictl-onboard` | command |
| `sei.io/engineer` | engineer alias from `~/.seictl/engineer.json` | always |
| `sei.io/bench-name` | the `--name` value | bench up |
| `tide.sei.io/cell-type` | `personal` | onboard (cells-forward) |
| `tide.sei.io/owner` | engineer alias | onboard (cells-forward) |

Queries:

- `bench list` → `app.kubernetes.io/part-of=seictl-bench,sei.io/engineer=<alias>` on SeiNodeDeployments
- `bench down --name X` → add `sei.io/bench-name=X`. Get SNDs first, delete with foreground propagation so children cascade in order.

### Landmine: PVC finalizers on `bench down`

`sei.io/seinode-finalizer` blocks SeiNode deletion until the controller releases PVCs cleanly. If the controller is unhealthy or EBS CSI flakes, children sit `Terminating` forever — eating quota and silently failing future `bench up --name X` on duplicate-name conflicts.

`bench down` issues delete with `metav1.DeletePropagationForeground` and a `context.WithTimeout(90s)`. On timeout: report still-terminating resources via JSON output, exit `category: "finalizer-stuck"`. Don't auto-patch finalizers, don't redirect to other tools — engineer decides recovery.

---

## Security cross-cuts

### Input validation

Single source of truth in `internal/validate/`. Commands call `validate.Alias(s)` etc. before any side effect.

| Input | Validation |
|---|---|
| `alias` | `^[a-z]([a-z0-9-]{0,28}[a-z0-9])?$` AND not in deny-list `{kube-system, kube-public, kube-node-lease, default, autobake, flux-system, istio-system, tide-agents}` |
| `name` | `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`. Combined `bench-<alias>-<name>` ≤ 63 chars (k8s label limit) |
| `--image` | (a) Parse with `name.ParseReference`. (b) Hostname **must equal** `189176372795.dkr.ecr.us-east-2.amazonaws.com`. (c) Repo prefix **must be** `sei/`. (d) Resolve to digest before render; fail closed |
| `--size` | Strict enum `s\|m\|l` |
| `--duration` | Integer minutes, `1 ≤ n ≤ 240` |
| `namespace` | RFC-1123 label. Must equal `eng-<engineer.alias>` for any side-effecting verb |

### IAM via AWS SDK (no Terraform)

`seictl onboard --apply` creates resources directly via AWS SDK:

1. **IAM policy** `harbor-bench-seiload-eng-<alias>` — per-engineer scoped to the shared validation-results bucket under the engineer's namespace prefix: `s3:ListBucket` with `s3:prefix=["eng-<alias>/*"]` and `s3:PutObject` on `arn:aws:s3:::harbor-validation-results/eng-<alias>/*`. Mirrors the platform's nightly policy (`harbor-nightly-seiload`); shared policies are explicitly rejected as a security risk that doesn't scale.
2. **Pod Identity association** via `eks:CreatePodIdentityAssociation` for `(cluster=harbor, namespace=eng-<alias>, service_account=bench-seiload, role_arn=<policy-attached-role>)`

Engineer's SSO role currently has admin permissions (sufficient to create the above). When SSO permissions get scoped down, the LLD revisits.

Offboarding (v1.1 or manual): mirror — `eks:DeletePodIdentityAssociation`, `iam:DeleteRole`, `iam:DeletePolicy`, plus revert PR.

### Identity file: `~/.seictl/engineer.json`

- Mode `0600`, parent dir `0700`. On read, refuse if perms loose.
- Two fields only: `alias`, `name`.
- Integrity matters: a loose-perms file on a shared workstation is a path to onboarding-as-someone-else or benching into another engineer's namespace.

### Image registry policy

Locked to ECR-only in v1: hostname literal `189176372795.dkr.ecr.us-east-2.amazonaws.com`, repo prefix `sei/`, always resolve to digest before render. Manifests always emit `image: <registry>/<repo>@sha256:<digest>` — never tag — to prevent registry typo-squat or tag-substitution attacks.

Future direction (out of scope for v1): per-engineer ECR namespace would let engineers test images they've built and pushed from a fork. Tracked separately.

---

## Embedded templates

`go:embed` a vendored copy under `templates/`. Each template carries a provenance header comment pointing at the upstream source-of-truth path in `sei-protocol/platform`:

```yaml
# Source: sei-protocol/platform autobake/templates/seinodedeployment.yaml
# Embedded at: 2026-04-27
# Template parameters: chainId, image, imageDigest, validatorCount, ...
apiVersion: sei.io/v1alpha1
kind: SeiNodeDeployment
...
```

Why embedded over live-fetch:

- Reproducible builds — no network at build time.
- Engineer iteration: bumping templates is a deliberate seictl release, not a silent surface change.

Drift discipline: when autobake's templates evolve, a seictl PR re-syncs the embedded copy. We don't try to enforce a CI drift check in v1 — the `seictl-bench` field manager makes drift between releases visible at apply time anyway.

Rendering: `text/template` — autobake source files use Helm-style `{{ }}`. Render to YAML, parse via `yaml.UnmarshalStrict` into Unstructured, apply via SSA.

---

## Exit codes

Eight codes. The granular cause lives in `ErrorBody.Category` (a stable string enum) — exit codes are families, not specifics. Nothing scripts against numeric ranges; readers branch on `category`.

| Code | Family | Meaning |
|---|---|---|
| 0 | | Success |
| 2 | usage | Usage error |
| 3 | not-found | Resource not found |
| 4 | cluster | Cluster unreachable |
| 5 | rbac | Permission denied |
| 10 | bench | Bench failure (specific cause in `error.category`) |
| 20 | onboard | Onboard failure (specific cause in `error.category`) |
| 40 | identity | Identity failure (specific cause in `error.category`) |

`error.category` enums (initial set; adding values is non-breaking):

- **bench:** `image-policy`, `image-resolution`, `validation`, `namespace-policy`, `apply-failed`, `name-collision`, `finalizer-stuck`, `template-render`
- **onboard:** `alias-invalid`, `platform-repo-missing`, `working-tree-dirty`, `gh-unauthenticated`, `pr-create-failed`, `aws-create-failed`
- **identity:** `malformed`, `missing`, `kubeconfig-parse`, `perms-loose`

---

## Build / test / lint

### Makefile additions

```make
lint-strict:
	golangci-lint run
```

### Test patterns

- Table-driven tests for flag parsing, name validation, exit-code mapping.
- `testdata/` golden files for rendered manifests. `go test -update` flag to refresh.
- Fake K8s client (`controller-runtime/pkg/client/fake`) for `bench list` and `bench down`.
- ECR resolver behind an interface — fake in tests, real in production. Same for git/gh in `onboard`.
- Integration tests under `integration/` build tag, gated on `SEICTL_INTEGRATION_CLUSTER` env, run in CI nightly.

### golangci-lint additions

Standard set already in seictl: `errcheck`, `govet`, `staticcheck`, `revive`, `gosec`. Document import groups in `.golangci.yml` matching seictl's `CLAUDE.md` convention.

---

## v1 Definition of Done

1. `seictl context` returns a populated `ContextResult` JSON envelope on a real harbor kubeconfig — `cluster`, `engineer`, `awsAccount` populated.
2. `seictl bench up --image <ref> --name demo --size s --duration 5m` (no `--apply`) renders SND-validator (4 replicas), SND-RPC (1 replica), profile ConfigMap, seiload Job — golden-file equality against `testdata/bench-up-s.golden.yaml`. Exits `0`. Output includes `dryRun: true`.
3. `seictl bench up ... --apply` against a dev/harbor cluster creates all four resources, populates `appliedAt`, returns `0`. Subsequent `seictl bench list` shows the run.
4. `seictl bench up --name X` re-run while the previous bench is still alive: idempotent (no error, returns same result), or rejects with exit `10` (category `name-collision`) — confirmed behavior either way.
5. `seictl onboard --alias bdc --no-pr --apply` against a clean platform-repo checkout creates `clusters/harbor/engineers/bdc/{kustomization,namespace,bench-seiload-sa}.yaml` and writes `~/.seictl/engineer.json`. With `--apply` and `gh` authenticated: opens a PR, captures URL in result. With AWS SDK calls: creates IAM policy and Pod Identity association.
6. `~/.seictl/engineer.json` written with mode `0600`; loose-perms file rejected with exit `40` (category `perms-loose`).
7. `seictl bench up --image <bad-registry-ref>` exits `10` (category `image-policy`) without contacting ECR.

---

## Migration / drift / deprecation notes

- JSON schemas in this LLD are versioned via `Envelope.Version`. Bumping is a breaking change for the `sei-platform-engineer` skill. Keep `v1` stable through MCP graduation.
- Field manager `seictl-bench` is a one-way door — changing it abandons ownership of every existing object.
- Embedded templates: when re-synced from upstream, output changes should land in dedicated PRs; engineer-visible behavior should not silently change between seictl releases.
- `sei-k8s-controller/api/v1alpha1` dependency: revisit once the package is restructured. Migration from Unstructured to typed access changes internal code only — no CLI surface change, no schema change.

---

## Files referenced

This LLD synthesizes review from the sei-platform-engineer skill council against:

- `/Users/brandon/workspace/seictl/CLAUDE.md`, `/Users/brandon/workspace/seictl/main.go`, `/Users/brandon/workspace/seictl/{config,genesis,patch,serve,await}.go`
- `/Users/brandon/sei-k8s-controller/api/v1alpha1/{seinode,seinodedeployment}_types.go`
- `/Users/brandon/sei-k8s-controller/cmd/main.go:118-125`
- `/Users/brandon/tide-workspace/platform/.github/workflows/k8s_autobake.yml`
- `/Users/brandon/tide-workspace/platform/terraform/aws/189176372795/eu-central-1/harbor/autobake.tf`
- `/Users/brandon/tide-workspace/platform/clusters/harbor/sei-k8s-controller/manager-patch.yaml`

Skill draft: `sei-protocol/Tide#8` — depends on this LLD; SKILL.md and `references/` will refresh once schemas here merge.
