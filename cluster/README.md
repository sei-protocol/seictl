# cluster/ — engineer harness verbs

This subtree holds seictl's **engineer-facing harness for harbor workloads** —
verbs an engineer runs to provision, run, and inspect ephemeral testing
workloads on the shared harbor EKS cluster (benchmarks today; stress and
integration tests later).

It is intentionally quarantined from the rest of `seictl`, which is the
node-operator's tool (sidecar HTTP server, config/genesis patching,
state-sync). The two share a binary and a Go module today, but their
audiences, contracts, and lifecycles are different.

## Why it lives here (for now)

The shape of the eventual user-facing API — likely a generic CRD wrapping
benchmarks / stress tests / integration tests — is still emerging. Until
the workload abstraction stabilizes, we hand-roll per verb under
`cluster/`. When the abstraction crystallizes, this directory is the unit
that splits out into its own binary (and probably its own module).

Layout intent:

```
cluster/
  bench.go, context.go, …    # exported cli.Command vars (BenchCmd, ContextCmd)
  *_test.go                   # in-package tests
  internal/                   # importable only from cluster/...
    clioutput/                # JSON envelope contract (the MCP tool surface)
    identity/                 # ~/.seictl/engineer.json
    validate/                 # input policy
    kube/                     # kubeconfig loading
    aws/                      # STS GetCaller, ECR digest resolver
    render/                   # ${VAR} template renderer + YAML splitter
  templates/                  # vendored autobake templates
```

## Rules

- **New cluster-facing verb?** Add a top-level `*.go` here exporting a
  `cli.Command` var, register it in `../main.go`. Don't put cluster
  verbs at the repo root.
- **New shared helper used only by cluster verbs?** It goes in
  `cluster/internal/<package>/`. Go's `internal/` rules will keep
  node-ops code from accidentally depending on it.
- **Need something from node-ops?** That's a smell — surface it via a
  small parent-level package both can import, or copy the function. The
  whole point of this directory is that future-you can `git mv cluster
  ../seictl-harness` without needing to detangle.
- **Don't import `github.com/sei-protocol/seictl/internal/...`** from
  here. Those packages belong to node-ops and won't move with us.
