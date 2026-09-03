# seictl

CLI for Sei node operators and platform engineers. Packaged as a single Go binary (`ghcr.io/sei-protocol/seictl`) distributed via GoReleaser (native binaries) and Docker (distroless).

The sidecar server it used to host now lives in `sei-protocol/sei-k8s-controller` under `sidecar/`, published as `sei/sei-sidecar`. Its wire contract is the `sidecarapi` module there, which this CLI imports.

## Architecture

`seictl` carries two distinct surfaces today:

**Node-operator surface** (the original):
- **CLI commands**: `config patch`, `genesis patch`, `patch`, `await` (top-level files: `config.go`, `genesis.go`, `patch.go`, `await.go`)
- **Task client**: `task/` — submits, gets, and deletes sidecar tasks over the `sidecarapi` HTTP contract
- **Shadow reports**: `report.go`, `report_list.go` over `internal/shadow` and `internal/s3`
- **Internal**: `internal/patch/` (TOML/JSON merge-patch), `internal/rpc` (Tendermint RPC client), `internal/s3`, `internal/shadow`

**Engineer-harness surface**: two preset-driven command trees over the SeiNetwork + SeiNode CRDs — `seictl network {apply,get,list,delete,watch}` (genesis networks) and `seictl node {…}` (followers/RPC). Shared internals in `internal/cliutil` (output/errors/client/parse/watch) and `internal/seiapi` (GVK plumbing). The legacy `nodedeployment`/`nd` verb and `internal/snd` were removed in the SeiNodeDeployment clean-break (v0.1.0).

## Code Standards

### Go

- Write clear, self-documenting code. Prefer descriptive names over comments that restate what the code does.
- Comments should explain *why*, not *what*. Reserve them for non-obvious intent, trade-offs, constraints, or public API contracts. Do not use comments as section dividers, narration, or decoration.
- No unnecessary abstractions. Three similar lines are better than a premature helper.
- Functions should do one thing. If a function needs a comment explaining what each section does, it should be multiple functions.
- Keep functions short. A function that doesn't fit on one screen is usually doing too much.
- Names are the best documentation: `sidecarImage(node)` needs no comment, `si(n)` needs a rewrite.
- Error messages should provide enough context to diagnose without a debugger.
- Imports grouped: stdlib, external, then `github.com/sei-protocol/seictl`.
- All code must pass `gofmt -s`. Run `make fmt` before committing.

### Testing

- Tests use `testing` from the standard library. No assertion frameworks unless already established.
- Table-driven tests for any function with more than two interesting input variations.
- Test names should describe the scenario, not the function: `TestValidateCron/empty_string` over `TestValidateCronEmpty`.
- Run `make test` before submitting changes.

### API Client

- The sidecar's HTTP contract lives in `sei-k8s-controller/sidecarapi`, not here. `task/` imports `sidecarapi/client`, aliased as `sidecar` by convention.
- The OpenAPI spec and its generated client moved with it. Regenerate there, not here.
- A change to that contract is a cross-repo change: bump the `sidecarapi` dependency in `go.mod` after it lands.

## Build & Run

```bash
make build       # Build to ./build/seictl
make test        # Run all tests
make lint        # Check formatting
make fmt         # Auto-format
make generate    # Regenerate OpenAPI client
make clean       # Remove build artifacts
```
