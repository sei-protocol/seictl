# Bugbash: cluster

**Target path:** `cluster/`
**Started:** 2026-04-29
**Last updated:** 2026-04-29
**Experts:** kubernetes-specialist, platform-engineer, network-specialist, security-specialist, product-manager
**Status:** Pass 1 partial — discovery complete (35 candidates surfaced), challenger pass run on a 3-finding sample to validate the `/bugbash` skill mechanics. Not driven to convergence; ~32 candidates remain pending challenger across future sessions.

## Summary

_Populated at convergence. Run is not yet at convergence._

| Severity | Confirmed | Pending challenger |
|----------|-----------|--------------------|
| Critical | 0         | unknown            |
| High     | 0         | unknown            |
| Medium   | 0         | unknown            |
| Low      | 2         | unknown            |
| Refuted  | 1         | n/a                |

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

---

## Pending Challenger Pass (32 candidates)

The discovery phase produced 35 candidates. 3 were challenger-passed in pass 1 to validate skill mechanics; the remaining 32 are listed below, grouped by finder, awaiting future challenger passes. Severity tags are **finder-proposed** and not yet adversarially tested — pass-1 sampling showed finders tend to overstate severity (1 refuted, 2 downgraded, 0 confirmed-as-stated out of 3 challenged), so these tags should be treated as upper bounds.

### From kubernetes-specialist

- bench down deletes by `engineer+name` labels alone, with no `managed-by` scope — would delete any resource an engineer hand-labelled. _(High → re-test)_
- Re-applying `bench up` to existing chain with different spec hits Job immutability mid-cascade after SNDs already mutated. _(High → re-test)_
- `bench down` reports `DeletedAt: now` and `action: deleted` before foreground propagation completes; subsequent `bench up` collides with Terminating objects. _(High → re-test)_
- `render.SplitYAML` separator detection misses `--- ` (trailing whitespace), `--- # comment`, CRLF combinations. _(Medium → re-test)_
- `requireNamespace` short-circuits SSA on missing-RBAC or 5xx flake, masking better per-resource RBAC error. _(Medium → re-test)_
- `render.Render` uses `os.Expand` which honors bare `$VAR` form; latent footgun for any future template containing literal `$`. _(Medium → re-test)_

### From platform-engineer

- Identity write happens after PR is opened — failure leaves engineer half-onboarded with PR + IAM but no local identity. _(High → re-test)_
- `failOnboard` after a successful PR creation drops the PR URL on the floor (envelope has no `data` for error path). _(High → re-test)_
- `clioutput.EmitError` swallows marshal errors; truncated JSON envelope on closed-pipe stdout indistinguishable from valid error. _(Medium → re-test)_
- Aggregator update mutates working tree before PR creation; subsequent failure leaves committed feature branch and a re-run silently discards via `git checkout -B`. _(High → re-test)_
- `git checkout -B <branch>` overwrites any prior local-only commits on a same-named branch. _(High → re-test)_
- Identity perm check uses `Mode().Perm() & 0o077`, ignoring extended ACLs and ownership — shared-workstation leak vector. _(Medium → re-test)_
- No `ctx` propagation into `git`/`gh` shell-outs; Ctrl-C mid-onboard leaves partial PR state. _(High → re-test)_

### From network-specialist

- ECR digest resolution targets `us-east-2` while cluster is in `eu-central-1`; engineer SSO regional creds may not work. _(Medium → re-test)_
- STS `GetCallerIdentity` has no region pin; falls back to IMDS (1s+ hang on non-EC2 hosts) and may hit org-blocked global endpoint. _(Medium → re-test)_
- Hardcoded `onboardRegion`/`onboardAccount` constants with no caller-account verification; engineer logged into a different account silently misprovisions. _(High → re-test)_
- RPC service DNS `<chain-id>-rpc.<ns>.svc.cluster.local:8545` baked into seiload profile; controller-side Service contract implicit and unverified. _(High → re-test)_
- No NetworkPolicy in onboard manifests; engineer cell egress depends entirely on cluster-default posture. _(High → re-test)_
- Pod Identity association not awaited before bench can run; race on first invocation can produce silent S3 PutObject failure. _(High → re-test)_
- `kube.New` doesn't cross-check kubeconfig server URL vs. expected harbor cluster — same-named local context (e.g., `kind`) silently misroutes apply. _(High → re-test)_

### From security-specialist (excluding sec-1, written above)

- `${VAR}` substitution in `render.Render` does no escaping; a doctored alias rendered into label position can forge ownership labels. _(Critical → re-test)_
- `githubpr.CreatePR` writes engineer-controlled paths into platform repo without traversal anchoring; alias regex is the only guard. _(Medium → re-test)_
- `gh pr create --body` and `git commit -m` rely on `validate.Alias` regex as their only escape — fragile trust boundary. _(Medium → re-test)_
- AWS `creds_hint` concatenated into error envelopes; can leak profile / MFA / token metadata to MCP callers. _(Medium → re-test)_
- `kube.New` honors kubeconfig `users[].exec` plugin block; anyone who can write `$KUBECONFIG` gets arbitrary code execution as the engineer on the next cluster verb. _(High → re-test)_
- `ResolveDigest` parses ECR account+region from the user-supplied host string; trust boundary is "validate.Image always runs first," fragile across future verb additions. _(Medium → re-test)_

### From product-manager (excluding pm-1, written above)

- `context` verb name implies kubeconfig mutation but only does inspection; engineers running `context --context staging` will assume it switched. _(Medium → re-test)_
- `onboard` doesn't generate a kubeconfig despite the brief and onboarding story implying it does. _(Medium → re-test)_
- Sibling verbs disagree on namespace scoping: `bench up` rejects `--namespace`, `bench down` accepts but enforces `eng-<alias>`, `bench list` accepts arbitrary namespace. _(Medium → re-test)_
- `bench list --all-namespaces` and `Owner` field advertise cross-engineer surface but the selector forces single-engineer scope — vestigial or under-documented. _(Medium → re-test)_
- Cluster constants (`harbor`, `eu-central-1`, ECR account) inlined across 5+ files; future second-cluster onboarding has no single source of truth. _(Medium → re-test)_
- `bench up` returns `chainId` in envelope, but no command accepts a chain-id as a handle — agent callers must reverse-engineer the alias-prefix scheme. _(Medium → re-test)_

## Refuted in Pass 1

- **`Apply` has no rollback on partial failure** (kubernetes-specialist) — refuted by platform-engineer: the docstring's "all-or-nothing" refers to the return-value contract (no partial `[]ApplyResult`), not cluster-state transactionality. SSA over multiple objects has no transaction primitive in K8s; the LLD explicitly chooses "no partial-state tracking" with re-run as the recovery path, and `bench down` cleans up by label whether the prior apply succeeded or failed. The candidate misread the docstring; not a bug. The sentence preceding "all-or-nothing" already disambiguates.
