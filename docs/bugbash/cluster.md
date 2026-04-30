# Bugbash: cluster

**Target path:** `cluster/`
**Started:** 2026-04-29
**Last updated:** 2026-04-29
**Experts:** kubernetes-specialist, platform-engineer, network-specialist, security-specialist, product-manager
**Status:** Pass 2 partial. Pass 1 surfaced 35 raw candidates and processed 3 (yielding the 2 Lows below + 1 refutation). Pass 2 ran the merge phase on the 32 remaining candidates (32 → 31, one merge: `os.Expand` footgun + `${VAR}` injection both reduce to "render.Render is non-defensive") and then sampled 10 merged candidates through parallel challengers under the refined skill (Tide PR #17). Pass 2 added 4 confirmed findings (Items 3-6: 2 High, 1 Medium, 1 Low) and 7 refutations. Run still not at convergence; ~22 candidates remain pending challenger.

## Summary

_Populated at convergence. Run is not yet at convergence._

| Severity | Confirmed | Pending challenger (upper bound) |
|----------|-----------|----------------------------------|
| Critical | 0         | 0                                |
| High     | 2         | unknown — finders inflate; pass-2 sample saw 60% refute/downgrade |
| Medium   | 1         | unknown                          |
| Low      | 3         | unknown                          |
| Refuted  | 7         | n/a                              |

## Findings

## Item 1: Re-validate engineer alias on identity.Read so all downstream verbs share a single trust boundary

### Overview

#### Experts involved

- **Finder:** security-specialist
- **Challenger:** kubernetes-specialist — verdict: downgrade from implied Critical to Low
- **Severity:** Low

### Scenario

`identity.Read` validates only that `Alias != ""` before returning the engineer struct. Downstream verbs (`bench`, `bench-down`, `bench-list`, `context`) consume `eng.Alias` to compose namespaces, label selectors, IAM role names, and S3 prefixes, but never re-run `validate.Alias` on the value. An attacker who already has shell access as the engineer (and so passed the `0700` parent-dir + `0600` file perm check) can edit `~/.seictl/engineer.json` to a doctored alias like `"kube-system"` or `"../etc"`.

### Impact / Risk / Priority

The original framing argued for namespace squatting, IAM-name spoofing, and S3-prefix escape. The challenger pass refuted most of that surface: the `eng-` prefix means doctored aliases collide with `eng-kube-system`, not `kube-system`; `validate.Namespace` (called inside `bench` and `bench-down`) catches pathological shapes like `*` and `../etc`; the K8s API server rejects malformed label-selector values server-side; and the threat actor model already requires local-shell-as-engineer, at which point AWS creds, kubeconfig, and SSH keys are all directly available without bothering with alias laundering. The genuine residual gap is `bench-list.go`, which neither calls `validate.Namespace` nor `validate.Alias`, but its worst outcome is an empty result or 4xx — not silent compromise.

### Issue

`/Users/brandon/tide-workspace/seictl/cluster/internal/identity/identity.go:65` — `Read` short-circuits validation. Consumer call sites: `bench.go:140-151`, `bench_down.go:85-93`, `bench_list.go:96-98`, `context.go:75-84`.

**Fix sketch:**

In `identity.Read`, after the `Alias != ""` check, call `validate.Alias(e.Alias)` and return the error. This closes the gap uniformly for every consumer (including `bench-list` and `context`, which today have no guard at all) without each verb needing to remember to re-validate. One change, all consumers protected.

**Test coverage:**

Unit test in `identity_test.go` that writes an identity file with a deny-listed alias (`kube-system`), calls `Read`, and asserts the validation error is returned. Add a sibling test for `..`, `*`, and an empty-but-only-whitespace alias.

## Item 2: Add a `--dry-run` flag to `bench down` to close the sibling-verb convention gap without inverting the destructive default

### Overview

#### Experts involved

- **Finder:** product-manager
- **Challenger:** kubernetes-specialist — verdict: downgrade from implied High to Low
- **Severity:** Low

### Scenario

`bench up` and `onboard` both default to dry-run and require `--apply` to take destructive action; `bench down` deletes the moment it's invoked, with no preview, confirmation, or `--apply` flag. An engineer who has internalized the "verbs are dry-run by default" model from the two sibling commands and runs `seictl bench down --name demo` to inspect what it would do will instead find their workload silently torn down.

### Impact / Risk / Priority

The original framing argued this was an operational footgun. The challenger pass downgraded it: convention across the broader ecosystem (`rm`, `kubectl delete`, `helm uninstall`, `terraform destroy`, `kubectl drain`) cuts the other way — destroy verbs default to acting. The "verbs are safe by default" mental model is a `seictl`-local convention established by exactly two sibling create-verbs, and the blast radius is bounded: bench down is engineer-namespace-scoped, requires `--name`, no-ops on NotFound, and the worst case is a re-runnable `bench up` to recover. That makes it a UX inconsistency, not a launch-blocking footgun.

### Issue

`/Users/brandon/tide-workspace/seictl/cluster/bench_down.go:63-78` — no `--dry-run` or `--apply` flag in the `cli.Command` definition. Acts immediately on invocation.

**Fix sketch:**

Add a `--dry-run` flag (opt-in preview) to `bench down`, mirroring `kubectl delete --dry-run=server` semantics. Do **not** invert the default to `--apply`-required — that would be a breaking change to any existing automation and isn't warranted given the bounded blast radius. The opt-in flag closes the mental-model mismatch without breaking muscle memory.

**Test coverage:**

Unit test that invokes `bench down --dry-run --name <existing>` and asserts that no `Delete` calls are issued against the kube client (mock the kube client at the `kube.Client` boundary), and that the JSON envelope reports the resources that *would* have been deleted. Add a sibling test that `--dry-run` on a non-existent bench name still returns the same not-found shape it does today.

## Item 3: Fail-fast on Job spec mutation in `bench up` to prevent split-state cluster

### Overview

#### Experts involved

- **Finder:** kubernetes-specialist
- **Challenger:** platform-engineer — verdict: confirm
- **Severity:** High

### Scenario

`bench up --image <new> --duration <new>` against an in-flight bench (same `--name`) reaches the K8s apply pipeline with a Job whose spec has changed in immutable fields. The render pipeline produces docs in a stable order — `cmYAML, sndYAML, jobYAML` — and `kube.Apply` walks them sequentially via `Visit`. The validator and RPC `SeiNodeDeployment` apply succeed and bump the seid image; the seiload Job apply then hits the K8s Job validator, which forbids mutations to `spec.template`, `spec.completions`, `spec.parallelism`, `spec.selector`, and (in some apiserver versions) `activeDeadlineSeconds`, returning 422 invalid.

### Impact / Risk / Priority

The cluster is left with new validator/RPC images racing against the previous bench's still-running seiload Job — measurements get attributed to the new run via labels and S3 path, but the workload generator on the data-producing side is still the old one. The error surfaces only as `clioutput.CatApplyFailed` ("apply-failed") with no Job-immutability hint and no signal that a partial mutation already happened. Recoverable via `seictl bench down --name <name>` followed by re-`bench up`, but only by an engineer who diagnoses the split state. High under the rubric: silent partial-state corruption of a running bench, recoverable only by an informed engineer.

### Issue

`/Users/brandon/tide-workspace/seictl/cluster/bench.go:195-210` (the apply call site), `/Users/brandon/tide-workspace/seictl/cluster/internal/kube/apply.go:42-79` (the sequential Visit loop). The seiload Job template at `cluster/templates/seiload-job.yaml` carries fields that vary across re-runs: `activeDeadlineSeconds: ${JOB_DEADLINE_SECONDS}`, `IMAGE_DIGEST_SHORT` env, and the `sei.io/image-sha` pod-template label all change when `--image` or `--duration` change. The "all-or-nothing" docstring on `Apply` (line 42) describes a return-value contract, not a transactional cluster-state guarantee — Kubernetes provides no transaction primitive across multiple objects, but the test in `bench_test.go` only verifies idempotency on unchanged input.

**Fix sketch:**

In `kube.Apply`, add a pre-flight phase before the Visit loop. For each rendered doc whose Kind matches a known immutable resource (`Job` is the immediate concern; `PersistentVolumeClaim` and `Service.spec.clusterIP` are similar), do a `Get` and compare the relevant immutable fields against the rendered spec. If any differ, fail fast with an actionable error: `"Job seiload-<chain-id> has immutable field changes (template / activeDeadlineSeconds); run 'seictl bench down --name <name>' first"`. This keeps the all-or-nothing contract honest by aborting before any SND is patched. Alternatively, `bench up` could refuse outright when an existing chain is detected with different spec, surfacing the choice to the operator.

**Test coverage:**

Integration test that creates a bench, then re-runs `bench up` with `--duration 600s` (different from the original 300s) and asserts (a) the operation fails before any SND is mutated, (b) the error message names "Job" and "immutable", and (c) the cluster state matches the pre-run snapshot. Add to `cluster/bench_test.go` alongside the existing idempotency test.

## Item 4: Don't claim deletion is complete until foreground propagation has actually finished

### Overview

#### Experts involved

- **Finder:** kubernetes-specialist
- **Challenger:** product-manager — verdict: confirm
- **Severity:** High

### Scenario

`seictl bench down --name <name>` issues parallel deletes for SeiNodeDeployments, Jobs, and ConfigMaps in the engineer's namespace, all with foreground propagation. `helper.DeleteWithOptions` returns as soon as the apiserver accepts the deletion request (sets `deletionTimestamp` + finalizers); foreground propagation only blocks the *parent's removal* until children are gone, not the API call itself. The CLI envelope's `DeletedAt` field is unconditionally stamped to wall-clock `now`, and every `DeleteResult.Action` is hard-coded to `"deleted"` (or `"not-found"`). The "still-terminating" action enumerated in the docstring is unreachable.

### Impact / Risk / Priority

The documented operator workflow is `bench down` → `bench up` to reset a benchmark. Running them back-to-back means SSA fires against still-Terminating SeiNodeDeployments and ConfigMaps, producing 409/422 from the apiserver — surfacing as `clioutput.CatApplyFailed` with no hint that the operator should wait. SeiNodeDeployment finalizers can take minutes if the controller is slow; the lag is invisible. High under the rubric: operational footgun on the documented up/down cycle, single-engineer blocking, recoverable only by waiting and retrying with no signal telling the operator to wait. Compounded by a documentation/contract drift (the unreachable `still-terminating` action enum).

### Issue

`/Users/brandon/tide-workspace/seictl/cluster/internal/kube/delete.go:62-80` (where `Action` is hard-coded to `"deleted"` regardless of finalizer state), `/Users/brandon/tide-workspace/seictl/cluster/bench_down.go:124-130` (where `DeletedAt` is stamped unconditionally), and the docstring drift at `delete.go:19` (which enumerates `"still-terminating"` as a possible action that is never emitted).

**Fix sketch:**

After `helper.DeleteWithOptions` succeeds, re-`Get` the object. If it exists with a non-nil `deletionTimestamp`, emit `Action: "still-terminating"` instead of `"deleted"`, and either drop `DeletedAt` from that result or rename it to `DeletionRequestedAt`. In `bench_down.go`, only stamp `DeletedAt` on the envelope when every result is `"deleted"` or `"not-found"`. When any result is `"still-terminating"`, surface a hint in the envelope: `"Resources still terminating; wait <N>s before re-applying."`

**Test coverage:**

Unit test in `cluster/internal/kube/delete_test.go` that mocks the kube client to return a still-Terminating resource on the post-delete `Get` and asserts the `DeleteResult.Action` is `"still-terminating"`, not `"deleted"`. End-to-end test (or integration with envtest) that runs `bench down` against a SeiNodeDeployment with a slow finalizer and asserts the envelope reports the still-terminating state.

## Item 5: Verify caller AWS account matches `onboardAccount` before creating IAM resources

### Overview

#### Experts involved

- **Finder:** network-specialist
- **Challenger:** product-manager — verdict: downgrade from High to Medium
- **Severity:** Medium

### Scenario

`runOnboard` calls `deps.getCaller(ctx)` early to load the caller's AWS account, then constructs IAM Policy ARNs, Role ARNs, Pod Identity SourceArns, and the trust policy's `aws:SourceAccount` condition entirely from the hardcoded constant `onboardAccount = "189176372795"`. The caller's actual account is never compared to that constant. An engineer whose AWS profile points at a different account (sandbox, staging, personal) runs `seictl onboard` and IAM accepts every call: `CreatePolicy`, `CreateRole`, and the trust policy with `aws:SourceAccount: 189176372795` are all created in the *caller's* account, with cross-account ARN references that AWS treats as opaque strings.

### Impact / Risk / Priority

The resulting role can never be assumed (the trust policy names a SourceAccount that doesn't match the actual cluster's), and the PR opened against `sei-protocol/platform` references a non-existent cell. The engineer doesn't see a failure until `bench up` tries to use the resources hours or days later. Medium under the rubric: narrow failure mode (only fires when the caller is logged into the wrong account), recoverable by deleting the orphaned IAM artifacts and the PR, no security exposure (the role is unassumable, so no privilege flows through it).

### Issue

`/Users/brandon/tide-workspace/seictl/cluster/onboard.go:25-29` (the constants), `:126` (where `getCaller` already loads `caller.Account` but the result is unused for verification), `cluster/internal/aws/iam.go:236-254` (where `scope.Account` is baked into the trust policy as `aws:SourceAccount` and `aws:SourceArn`).

**Fix sketch:**

Add a 3-line guard immediately after the existing `getCaller` call: `if caller.Account != onboardAccount { return failOnboard(... clioutput.CatWrongAccount ...) }`. Introduce `CatWrongAccount` (or reuse `CatAWSUnavailable` with a more specific message) so the failure is loud and actionable: `"AWS caller account <X> does not match expected harbor account <Y>; switch profiles and retry."` The `getCaller` call is already there; the cost is genuinely about three lines.

**Test coverage:**

Unit test in `cluster/onboard_test.go` that injects a `getCaller` returning a non-harbor account and asserts `runOnboard` returns the new error category before any IAM call is issued. Verify the existing happy-path test still passes (caller account matches `onboardAccount`).

## Item 6: Refuse `git checkout -B` on a branch with local commits ahead of its tracking ref

### Overview

#### Experts involved

- **Finder:** platform-engineer
- **Challenger:** security-specialist — verdict: downgrade from High to Low
- **Severity:** Low

### Scenario

After `CheckCleanTree` and `EnsureBaseUpToDate` confirm the working tree is clean and `origin/<base>` is fetched, `CreatePR` runs `git checkout -B <branch> origin/<base>`. The `-B` flag force-resets the named branch ref to the new starting point. If a same-named local branch (`seictl/onboard-<alias>`) exists from a prior failed onboard run with local-only commits that never reached the remote (push failure, or a PR opened then closed without merge, then more commits added), those commits are silently overwritten by the reset.

### Impact / Risk / Priority

Reaching the loss path requires a narrow conjunction: prior `seictl onboard` failed *after* commit but with no open PR; the engineer added more commits to that exact branch name; the engineer re-runs onboard from a clean working tree. The `findOpenPR` short-circuit at `githubpr.go:101` defends the most likely partial-state case (push succeeded, PR is open) by returning early before the `-B`. Anything not pushed is in `git reflog` for ~90 days by default, recoverable. Low under the rubric: narrow failure mode, recoverable inconvenience rather than data loss, branch namespace (`seictl/onboard-<alias>`) makes collision with hand-authored work unlikely.

### Issue

`/Users/brandon/tide-workspace/seictl/cluster/internal/githubpr/githubpr.go:108` — the `git checkout -B` call has no pre-flight check for whether the existing local branch carries commits ahead of its remote tracking ref.

**Fix sketch:**

Before line 108, add a guard: `git rev-parse --verify --quiet refs/heads/<branch>` to detect existing local branch; if present, run `git rev-list --count origin/<branch>..<branch>` to count commits ahead. If the count is non-zero, refuse with a message naming `git reflog` and `git switch <branch>` as recovery options. Cost is one extra subprocess call when the branch doesn't already exist (the common case) and a trivial check when it does.

**Test coverage:**

Unit test in `cluster/internal/githubpr/githubpr_test.go` (using a `testenv` git fixture) that creates a local branch with one commit ahead of `origin/main`, then invokes `CreatePR` and asserts it returns an error before `checkout -B` runs. Sibling test confirms the happy path (no existing branch) still proceeds.

---

## Pending Challenger Pass (~22 candidates remaining)

The discovery phase produced 35 candidates. Pass 1 processed 3 (yielding Items 1, 2, and 1 refutation). Pass 2 applied the merge phase (32 → 31, one merge: `render.Render` non-defensive — both source candidates refuted in challenger) and sampled 10 merged candidates through parallel challengers (yielding Items 3-6 and 6 more refutations). The candidates listed below are the remaining ~22 grouped by finder, awaiting future challenger passes.

Severity tags are **finder-proposed** and not yet adversarially tested. Across both sampled passes (3 in pass 1, 10 in pass 2), the refute/downgrade rate has been ~70%, so finder-proposed tags should be treated as upper bounds.

### From kubernetes-specialist

- bench down deletes by `engineer+name` labels alone, with no `managed-by` scope — would delete any resource an engineer hand-labelled. _(High → re-test)_
- `render.SplitYAML` separator detection misses `--- ` (trailing whitespace), `--- # comment`, CRLF combinations. _(Medium → re-test)_
- `requireNamespace` short-circuits SSA on missing-RBAC or 5xx flake, masking better per-resource RBAC error. _(Medium → re-test)_

### From platform-engineer

- `failOnboard` after a successful PR creation drops the PR URL on the floor (envelope has no `data` for error path). _(High → re-test)_
- `clioutput.EmitError` swallows marshal errors; truncated JSON envelope on closed-pipe stdout indistinguishable from valid error. _(Medium → re-test)_
- Aggregator update mutates working tree before PR creation; subsequent failure leaves committed feature branch and a re-run silently discards via `git checkout -B`. _(High → re-test)_
- Identity perm check uses `Mode().Perm() & 0o077`, ignoring extended ACLs and ownership — shared-workstation leak vector. _(Medium → re-test)_
- No `ctx` propagation into `git`/`gh` shell-outs; Ctrl-C mid-onboard leaves partial PR state. _(High → re-test)_

### From network-specialist

- ECR digest resolution targets `us-east-2` while cluster is in `eu-central-1`; engineer SSO regional creds may not work. _(Medium → re-test)_
- STS `GetCallerIdentity` has no region pin; falls back to IMDS (1s+ hang on non-EC2 hosts) and may hit org-blocked global endpoint. _(Medium → re-test)_
- RPC service DNS `<chain-id>-rpc.<ns>.svc.cluster.local:8545` baked into seiload profile; controller-side Service contract implicit and unverified. _(High → re-test)_
- `kube.New` doesn't cross-check kubeconfig server URL vs. expected harbor cluster — same-named local context (e.g., `kind`) silently misroutes apply. _(High → re-test)_

### From security-specialist (excluding sec-1, written above)

- `githubpr.CreatePR` writes engineer-controlled paths into platform repo without traversal anchoring; alias regex is the only guard. _(Medium → re-test)_
- `gh pr create --body` and `git commit -m` rely on `validate.Alias` regex as their only escape — fragile trust boundary. _(Medium → re-test)_
- AWS `creds_hint` concatenated into error envelopes; can leak profile / MFA / token metadata to MCP callers. _(Medium → re-test)_
- `ResolveDigest` parses ECR account+region from the user-supplied host string; trust boundary is "validate.Image always runs first," fragile across future verb additions. _(Medium → re-test)_

### From product-manager (excluding pm-1, written above)

- `context` verb name implies kubeconfig mutation but only does inspection; engineers running `context --context staging` will assume it switched. _(Medium → re-test)_
- Sibling verbs disagree on namespace scoping: `bench up` rejects `--namespace`, `bench down` accepts but enforces `eng-<alias>`, `bench list` accepts arbitrary namespace. _(Medium → re-test)_
- `bench list --all-namespaces` and `Owner` field advertise cross-engineer surface but the selector forces single-engineer scope — vestigial or under-documented. _(Medium → re-test)_
- Cluster constants (`harbor`, `eu-central-1`, ECR account) inlined across 5+ files; future second-cluster onboarding has no single source of truth. _(Medium → re-test)_
- `bench up` returns `chainId` in envelope, but no command accepts a chain-id as a handle — agent callers must reverse-engineer the alias-prefix scheme. _(Medium → re-test)_

## Refuted

### Pass 1

- **`Apply` has no rollback on partial failure** (kubernetes-specialist) — refuted by platform-engineer: the docstring's "all-or-nothing" refers to the return-value contract (no partial `[]ApplyResult`), not cluster-state transactionality. SSA over multiple objects has no transaction primitive in K8s; the LLD explicitly chooses "no partial-state tracking" with re-run as the recovery path, and `bench down` cleans up by label whether the prior apply succeeded or failed. Not a bug.

### Pass 2

- **Identity write happens after PR succeeds → half-onboarded engineer** (platform-engineer) — refuted by security-specialist: re-running `seictl onboard --alias <same>` is fully recoverable. `ProvisionIAM` and `EnsurePodIdentity` are idempotent (return `"exists"` on re-run); `--no-pr` skips the cell-dir guard and goes straight to `writeIdentity`. The IAM + PR are durable shared state, the local identity is recreated cheaply — that asymmetry is the correct design. A missing local identity is fail-closed (`seictl bench` refuses rather than misattributing work), so no security exposure either.
- **No NetworkPolicy in onboard manifests** (network-specialist) — refuted by kubernetes-specialist: NetworkPolicy generation is not seictl's responsibility. The platform's harbor design (`platform/docs/designs/harbor-cilium.md`) explicitly chose `policyEnforcementMode: default`, matching stock K8s NetworkPolicy semantics; per-namespace policies are authored in the platform/Flux repo (precedent: `clusters/dev/tide-runners/network-policy.yaml`). The "IMDS bypass" sub-claim is also misframed: IMDS hardening is node-level (`httpPutResponseHopLimit=1`), and EKS Pod Identity uses link-local 169.254.170.23, not IMDS. A default-deny baseline for engineer cells belongs as a `CiliumClusterwideNetworkPolicy` in the platform repo's harbor base, and is already an open follow-up there.
- **render.Render is non-defensive: bare `$VAR` + unescaped substitution** (kubernetes-specialist + security-specialist, merged in pass-2 merge phase) — refuted by platform-engineer: both symptoms are individually blocked by existing controls. The bare `$VAR` form is caught by the fail-closed missing-vars block in `render.go:34-42` (loud `CatTemplateRender` error, not silent miss), and templates are vendored, not engineer-controlled. The YAML-injection symptom is blocked by `validate.Namespace(namespace, eng.Alias)` at `bench.go:151`, which requires the rendered namespace to match `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$` — rejecting newlines, quotes, colons, spaces, and `#` before any rendering happens. The two finder framings don't share a root cause; the merge across lenses was a false positive that the challenger correctly caught.
- **kubeconfig users[].exec plugin = arbitrary code exec** (security-specialist) — refuted by kubernetes-specialist: standard kubectl-equivalent behavior. `genericclioptions.NewConfigFlags(true).ToRawKubeConfigLoader()` is the canonical cli-runtime entrypoint that every K8s tool uses; honoring `users[].exec` is the documented mechanism for `aws eks get-token` and the entire managed-cluster auth ecosystem. The threat model ("attacker who can write `$KUBECONFIG`") already implies filesystem write as the engineer, which subsumes "wait for next cluster verb" — they can equivalently rewrite `~/.zshrc`, `~/.aws/config`, or drop a binary in `~/bin`. Tightening kubeconfig perms beyond kubectl-standard would break aws-eks-token-provider on first run with no real defense uplift.
- **onboard doesn't generate a kubeconfig** (product-manager) — refuted by platform-engineer: the verb's documented Usage is "Provision a new engineer's harbor footprint (IAM + namespace cell)" — no kubeconfig promise anywhere in the codebase. The "No Role/RoleBinding" decision is explicitly tracked at `onboardmanifests.go:6-8` ("engineers operate as cluster-admin via SSO today; per-engineer scoped K8s identity is tracked at sei-protocol/seictl#80"). Engineers obtain cluster access from SSO + `aws eks update-kubeconfig` before running `onboard`; the verb's actual contract (IAM + Pod Identity association + namespace cell PR) is delivered as documented. At most a Low documentation gap (top-level README doesn't list cluster-harness verbs).
- **Pod Identity association race vs. bench Job admission** (network-specialist) — refuted by kubernetes-specialist: between `onboard` and the first `bench up`, there's a human PR-merge step measured in hours-to-days (the cell manifests must be merged and reconciled by ArgoCD/Flux before the engineer's namespace and ServiceAccount even exist on-cluster), dwarfing any plausible EKS regional-consistency lag (sub-second in practice). Even in the contrived case where the association lookup lags pod admission, the Pod Identity webhook injects credentials at admission time — if the association isn't visible, env vars simply aren't injected and the AWS SDK fails with an explicit `NoCredentialProviders` / 403, not silently. The "silently fails" framing also conflates seictl's surface (which ends at `bench up` apply) with seiload's runtime error reporting.
