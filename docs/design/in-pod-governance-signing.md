# In-Pod Governance Transaction Signing for Sei Validators

**Status:** Draft (design)
**Scope:** Replace SSH+CLI governance signing (`slanders/seienv`) with in-pod sidecar-based signing for K8s-hosted Sei validators
**Authors:** Brandon Chatham (platform engineer); coral round dispatched kubernetes-specialist, blockchain-developer, platform-engineer
**Last updated:** 2026-05-11
**Issues:** sei-protocol/sei-k8s-controller#219 (CRD), sei-protocol/seictl#162 (keyring backend), #163 (sign-tx tasks), #164 (CLI), #165 (authn)
**Workstream:** governance flow migration — replace `seienv` (EC2 + SSH + `seid tx` shell-out) with `seictl` + sidecar

---

## Summary

`slanders/seienv` performs validator governance operations (`MsgVote`, `MsgSubmitProposal`, `MsgDeposit`) by SSH-ing into EC2 validator hosts and shell-ing out to `seid tx gov ...` against the host's local `~/.seid/keyring-file/`. The pattern doesn't translate to Kubernetes — pods don't have SSH, the seid container is being made distroless, and "no shells in pods" is a stated platform direction.

This design replaces that pattern with **library-based signing inside the seictl sidecar container**, using the Cosmos SDK packages already in seictl's `go.mod` (`sei-cosmos/client/tx`, `sei-cosmos/crypto/keyring`). The operator-account keyring lives in a Kubernetes Secret declared on the `SeiNode` CRD, mounted **only** on the sidecar container (never on the seid main container, never on bootstrap pods). The sidecar exposes typed task types (`gov-vote`, `gov-submit-proposal`, `gov-deposit`) over its existing HTTP task API. A new `seictl gov ...` subcommand surfaces this to human operators, with K8s-native authn (TokenReview) gating mutating endpoints in the final phase.

**Precedent is already in tree:** `sidecar/tasks/generate_gentx.go` already opens a `keyring.New(...)`, builds a Cosmos SDK tx, signs in-process, and persists the result. The genesis path uses `BackendTest`; the governance path uses `BackendFile`. Mechanics are identical; only the backend and the broadcast step differ.

The work is decomposed into 5 issues. Phase 1 (#219 + #162) is independent and runs in parallel. Phase 2 (#163) depends on both. Phase 3 (#164) depends on #163. Phase 4 (#165, authn) is a deliberate final hardening pass — the team explicitly opted to ship the core flow first, mirroring today's SSH-key-equivalent trust posture, and close the authn gap once the surface is proven against a genesis network.

## Goals

- **In-pod signing.** Operator-account keyring material never leaves the validator pod's sidecar container. No keys on laptops, no keys on bastion hosts, no keys on EC2 instances.
- **Library-based, not shell-out.** Use the Cosmos SDK packages directly. Preserve the "no shells in pods" architectural commitment. Avoid CLI-stdout-as-IPC fragility, version drift, and trust-boundary collapse.
- **Typed task surface.** Three explicit task types (`gov-vote`, `gov-submit-proposal`, `gov-deposit`) — not a generic "sign-and-broadcast" with raw Msg bytes. Better validation, better audit, easier authz when authn lands.
- **Idempotent, crash-safe.** Caller-supplied UUID dedupe at the engine, plus tx-hash persistence before broadcast at the handler — a retry after a successful broadcast NEVER produces a duplicate transaction.
- **K8s-native trust model.** TokenReview-based authn (Phase 4), `system:serviceaccount:…` allowlist, structured audit logs.
- **Single canonical operator tool.** `seictl gov vote ...` replaces `seienv vote ...`. Fan-out across a `SeiNodeDeployment`'s validators is a flag, not a fleet management tool.
- **Composes with existing genesis flow.** `generate_gentx.go`'s in-process signing pattern is the precedent; the new handlers extend the same shape with `BackendFile` and broadcast.

## Non-goals

- **Non-governance transactions.** Staking (`MsgDelegate`, `MsgUndelegate`), distribution (`MsgWithdrawDelegatorReward`), bank (`MsgSend`), IBC — same pattern, future issues if needed.
- **Remote signers.** TMKMS, Horcrux, Vault, AWS KMS, HSM — `OperatorKeyringSource` is a discriminated union with `Secret` as the only initial variant; siblings are reserved as comments only.
- **EVM-side operator transactions.** Sei's Cosmos-EVM address pairing is out of scope. EVM key material, contract ownership, EVM tx submission — not addressed here.
- **Per-SeiNode authz granularity.** Phase 4 ships a coarse allowlist of caller identities. "This SA can vote but not submit-proposal" is deferred.
- **Hot keyring rotation.** Passphrase change requires pod restart in v1. `POST /v0/admin/reload-keyring` is a v1.1 conversation.
- **Multi-tenant cluster deployment.** Phase 1-3 ship without authn; deployment posture is single-tenant operator-controlled. Multi-tenant deployment requires Phase 4 to land first.
- **CRD-managed operator-keyring Secret lifecycle.** The controller never creates, mutates, or deletes the Secret — operators ship it via SOPS / ESO / kubectl, same model as today's `signingKey` and `nodeKey`.

## Architecture

### Component map

```
┌──────────────────────────────────────────────────────────────────┐
│ Operator workstation                                             │
│  ┌────────────┐  kubeconfig + bearer token (Phase 4)             │
│  │ seictl gov │ ─────────────────────────┐                       │
│  └────────────┘                          │                       │
└──────────────────────────────────────────│───────────────────────┘
                                           │ HTTP POST /v0/tasks
                                           ▼
┌──────────────────────────────────────────────────────────────────┐
│ K8s Pod (managed by sei-k8s-controller as StatefulSet)           │
│                                                                  │
│  ┌──────────────────────────┐    ┌────────────────────────────┐  │
│  │ seictl sidecar           │    │ seid main container        │  │
│  │  (distroless)            │    │  (chain runtime)           │  │
│  │                          │    │                            │  │
│  │  ┌────────────────────┐  │    │ /sei/config/               │  │
│  │  │ keyring (in-mem)   │  │    │   priv_validator_key.json  │  │
│  │  │  opened at startup │  │    │   node_key.json            │  │
│  │  └────────────────────┘  │    │ /sei/data/                 │  │
│  │  /sei/keyring-file/      │    │                            │  │
│  │   <key>.info             │    │ RPC :26657                 │  │
│  │   <hex>.address          │    │  ◄────── localhost ────────┼──┘
│  └──────────────────────────┘    └────────────────────────────┘
│        ▲                                                         │
│        │ mount mode 0o400, sidecar only                          │
│  ┌─────┴─────────────────────────────────────────────────────┐   │
│  │ Secret (kubectl/SOPS/ESO) — operator-managed              │   │
│  │   keyring-file/<key>.info                                 │   │
│  │   keyring-file/<hex>.address                              │   │
│  └───────────────────────────────────────────────────────────┘   │
│  ┌───────────────────────────────────────────────────────────┐   │
│  │ Secret (separate) — passphrase only                       │   │
│  │   passphrase: <pw>                                        │   │
│  └───────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────┘
```

| Component | Repo | Issue |
|---|---|---|
| **A.** `SeiNode.validator.operatorKeyring` CRD field, validation task, sidecar-only mount | sei-k8s-controller | #219 |
| **B.** Sidecar keyring backend (envs, smoke test, factory) | seictl | #162 |
| **C.** Sign-tx task family (gov-vote, gov-submit-proposal, gov-deposit) | seictl | #163 |
| **D.** seictl `gov` CLI subcommands | seictl | #164 |
| **E.** Sidecar authn + authz middleware (final hardening) | seictl | #165 |

### Trust boundary

The operator-keyring Secret is mounted **only** on the sidecar container. Enforced at four layers:

1. **CRD admission** — CEL invariants reject configs that collapse the boundary
2. **Planner build-time** — only the sidecar-targeting pod-spec builder appends the volume/mount
3. **Pod-spec construction** — `buildSidecarContainer` is the only function that calls `operatorKeyringMounts`
4. **Runtime guard** — `assertNoValidatorSecretsOnBootstrapPod` fails closed if any validator Secret leaks into a bootstrap pod

The seid main container is intentionally unprivileged with respect to governance: it has the consensus key (for block signing) and the P2P node key, but **never** the operator account keyring. A compromised seid binary cannot vote on behalf of the validator.

### End-to-end sequence (gov vote)

```mermaid
sequenceDiagram
    autonumber
    participant Op as Operator<br/>(seictl CLI)
    participant K8s as K8s API server
    participant SC as seictl sidecar
    participant SD as seid<br/>(local RPC :26657)
    participant Chain as Chain

    Op->>K8s: GET SeiNode/<name>
    K8s-->>Op: spec + status
    Op->>Op: resolve sidecar URL<br/>(headless Service DNS)
    Op->>K8s: TokenRequest (Phase 4)
    K8s-->>Op: bearer token
    Op->>SC: POST /v0/tasks<br/>{type:gov-vote, params, taskId:UUID}<br/>Authorization: Bearer ... (Phase 4)
    SC->>K8s: TokenReview (Phase 4)
    K8s-->>SC: authenticated identity
    SC->>SC: dedupe by UUID;<br/>dispatch handler
    SC->>SD: GET /status
    SD-->>SC: node_info.network
    SC->>SC: chain-id guard:<br/>params.chainId == status.network
    SC->>SC: open keyring; resolve key
    SC->>SD: ABCI /auth.Query/Account
    SD-->>SC: accountNumber, sequence
    SC->>SC: build MsgVote;<br/>tx.Factory; Sign
    SC->>SC: compute txHash =<br/>sha256(txBytes);<br/>persist {taskId, txHash}
    SC->>SD: BroadcastTxSync(txBytes)
    SD-->>SC: TxResponse{code, txHash}
    SC->>SD: poll /tx?hash=...
    SD-->>SC: txResult{height, gasUsed}
    SC->>SC: persist final result
    Op->>SC: GET /v0/tasks/{id} (poll)
    SC-->>Op: {status:completed, txHash, height}
```

On crash before step 17 (broadcast), the engine restart re-runs the handler with the same UUID. The handler queries `/tx?hash=<persistedHash>`:

- If found → success; populate result from chain
- If not found AND sequence has not advanced → safe to re-sign and re-broadcast
- If not found AND sequence has advanced → tx may have been DeliverTx-rejected; surface as terminal error

---

## Component A — CRD `validator.operatorKeyring` (sei-k8s-controller)

### Go type definitions

Add to `api/v1alpha1/validator_types.go` after the existing `SecretNodeKeySource` (line ~110):

```go
// OperatorKeyringSource declares where a validator's operator-account
// keyring (used by the sidecar to sign governance, MsgEditValidator,
// withdraw-rewards, and other operator-account transactions) comes from.
// Exactly one variant must be set; variants are mutually exclusive.
//
// +kubebuilder:validation:XValidation:rule="(has(self.secret) ? 1 : 0) == 1",message="exactly one operator keyring source must be set"
type OperatorKeyringSource struct {
    // Secret loads a Cosmos SDK file-backend keyring from a Kubernetes Secret
    // in the SeiNode's namespace.
    // +optional
    Secret *SecretOperatorKeyringSource `json:"secret,omitempty"`

    // Future siblings: KMS, Vault, TMKMS-style remote signers.
}

// SecretOperatorKeyringSource references the Kubernetes Secrets that supply
// the operator-account keyring directory and its unlock passphrase.
//
// SecretName references the Secret containing the on-disk file-keyring
// (`<keyName>.info` plus `<hex>.address` index files). The Secret's data
// keys are projected as-is under $SEI_HOME/keyring-file/ on the sidecar
// container only; the seid main container never sees this material, and
// neither does the bootstrap pod.
//
// PassphraseSecretRef references a separate Secret carrying the passphrase
// used to decrypt the file-keyring. It is projected as an env var on the
// sidecar container only. The two Secrets are deliberately separate: the
// keyring Secret is a directory-shaped volume mount; co-locating the
// passphrase as a data key would cause it to be projected as a file under
// the keyring directory.
//
// The controller never creates, mutates, or deletes either Secret — their
// lifecycles are fully external (kubectl + SOPS, ESO, CSI Secrets Store).
type SecretOperatorKeyringSource struct {
    // SecretName names a Secret in the SeiNode's namespace whose data keys
    // are the on-disk Cosmos SDK file-keyring layout. Minimum required:
    //   <keyname>.info        (armored encrypted key blob)
    //   <hex-of-address>.address  (name→address index)
    //
    // +kubebuilder:validation:MinLength=1
    // +kubebuilder:validation:MaxLength=253
    // +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
    // +kubebuilder:validation:XValidation:rule="self == oldSelf",message="secretName is immutable"
    SecretName string `json:"secretName"`

    // KeyName is the name of the keyring entry to use when signing
    // (i.e. the name passed to `seid keys add <name>`). Defaults to
    // "node_admin" to preserve continuity with the seienv convention.
    //
    // +optional
    // +kubebuilder:default="node_admin"
    // +kubebuilder:validation:MaxLength=64
    // +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_-]+$`
    KeyName string `json:"keyName,omitempty"`

    // PassphraseSecretRef names a separate Secret containing the keyring
    // unlock passphrase. Required for the file backend.
    PassphraseSecretRef PassphraseSecretRef `json:"passphraseSecretRef"`
}

// PassphraseSecretRef points at a single key inside a Secret.
type PassphraseSecretRef struct {
    // SecretName names the passphrase Secret in the SeiNode's namespace.
    //
    // +kubebuilder:validation:MinLength=1
    // +kubebuilder:validation:MaxLength=253
    // +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
    // +kubebuilder:validation:XValidation:rule="self == oldSelf",message="passphrase secretName is immutable"
    SecretName string `json:"secretName"`

    // Key is the data key inside the Secret. Defaults to "passphrase".
    //
    // +optional
    // +kubebuilder:default="passphrase"
    // +kubebuilder:validation:MaxLength=253
    Key string `json:"key,omitempty"`
}
```

### Modification to `ValidatorSpec`

Insert after the `NodeKey` field (line ~37); add new struct-level XValidation rules alongside the existing ones at lines 6-7:

```go
// +kubebuilder:validation:XValidation:rule="!has(self.operatorKeyring) || !has(self.signingKey) || self.operatorKeyring.secret.secretName != self.signingKey.secret.secretName",message="operatorKeyring and signingKey must reference distinct Secrets — collapsing them into one Secret would force the sidecar/seid trust boundary to evaporate"
// +kubebuilder:validation:XValidation:rule="!has(self.operatorKeyring) || !has(self.nodeKey) || self.operatorKeyring.secret.secretName != self.nodeKey.secret.secretName",message="operatorKeyring and nodeKey must reference distinct Secrets"
// +kubebuilder:validation:XValidation:rule="!has(self.operatorKeyring) || self.operatorKeyring.secret.secretName != self.operatorKeyring.secret.passphraseSecretRef.secretName",message="operatorKeyring data Secret and passphrase Secret must be distinct"
type ValidatorSpec struct {
    // ... existing fields ...

    // OperatorKeyring declares the source of this validator's operator-account
    // keyring used by the sidecar to sign and broadcast governance, MsgEditValidator,
    // withdraw-rewards, and other operator-account transactions.
    //
    // Independently optional from signingKey/nodeKey: a validator may run as a
    // non-signing observer with operatorKeyring set (governance-only operations),
    // or as a consensus-signing validator without operatorKeyring (governance
    // performed out-of-band).
    //
    // Mounted exclusively on the sidecar container; seid main container and
    // bootstrap pods never carry this material.
    //
    // +optional
    OperatorKeyring *OperatorKeyringSource `json:"operatorKeyring,omitempty"`
}
```

The four CEL rules together enforce the trust boundary:
1. `operatorKeyring.secret.secretName ≠ signingKey.secret.secretName`
2. `operatorKeyring.secret.secretName ≠ nodeKey.secret.secretName`
3. `operatorKeyring.secret.secretName ≠ passphraseSecretRef.secretName`
4. `secretName` and `passphraseSecretRef.secretName` are immutable post-creation

### `OperatorKeyringReady` condition

Append to the const blocks in `api/v1alpha1/seinode_types.go`:

```go
const (
    // ... existing conditions ...

    // ConditionOperatorKeyringReady indicates whether a referenced operator-keyring
    // Secret pair (keyring data + passphrase) passes pre-flight validation. Only
    // set on SeiNodes with spec.validator.operatorKeyring.
    ConditionOperatorKeyringReady = "OperatorKeyringReady"
)

// Reasons for the OperatorKeyringReady condition.
const (
    ReasonOperatorKeyringValidated = "OperatorKeyringValidated" // success
    ReasonOperatorKeyringNotReady  = "OperatorKeyringNotReady"  // transient: retry
    ReasonOperatorKeyringInvalid   = "OperatorKeyringInvalid"   // terminal: fail the plan
)
```

### `validate-operator-keyring` task

New file `internal/task/validate_operator_keyring.go`, modeled on `validate_signing_key.go`:

```go
const TaskTypeValidateOperatorKeyring = "validate-operator-keyring"

type ValidateOperatorKeyringParams struct {
    SecretName           string `json:"secretName"`
    KeyName              string `json:"keyName"`
    PassphraseSecretName string `json:"passphraseSecretName"`
    PassphraseSecretKey  string `json:"passphraseSecretKey"`
    Namespace            string `json:"namespace"`
}

func (e *validateOperatorKeyringExecution) Execute(ctx context.Context) error {
    node, err := ResourceAs[*seiv1alpha1.SeiNode](e.cfg)
    if err != nil {
        return Terminal(err)
    }
    err = e.validate(ctx, node)
    switch {
    case err == nil:
        setOperatorKeyringCondition(node, metav1.ConditionTrue,
            seiv1alpha1.ReasonOperatorKeyringValidated,
            fmt.Sprintf("Secret pair (%q, %q) passes operator-keyring validation",
                e.params.SecretName, e.params.PassphraseSecretName))
        e.complete()
        return nil
    case isTerminal(err):
        setOperatorKeyringCondition(node, metav1.ConditionFalse,
            seiv1alpha1.ReasonOperatorKeyringInvalid, err.Error())
        return err
    default:
        setOperatorKeyringCondition(node, metav1.ConditionFalse,
            seiv1alpha1.ReasonOperatorKeyringNotReady, err.Error())
        return nil
    }
}

// validate enforces:
//  - both Secrets exist in node.Namespace and are not being deleted
//  - keyring Secret has at least one *.info data key (Terminal if empty)
//  - keyring Secret has at least one *.address index file (Terminal if malformed)
//  - if KeyName specified: an entry "<KeyName>.info" exists (Terminal otherwise)
//  - passphrase Secret has the named data key, non-empty (Terminal otherwise)
//
// NB: this task does NOT attempt to decrypt the keyring with the passphrase
// (that would require a kebab-instance of the Cosmos SDK keyring backend
// inside the controller process, which exceeds the controller's TCB).
// Decryption is the sidecar's startup smoke test (see Component B).
```

Categorize errors as terminal vs transient via the same pattern as `validate_signing_key.go`:
- Terminal: missing data key, malformed shape, empty values, named key absent
- Transient: Secret not found (operator may not have applied yet), Secret being deleted

### Planner integration

In `internal/planner/planner.go`:

```go
func needsValidateOperatorKeyring(node *seiv1alpha1.SeiNode) bool {
    return node.Spec.Validator != nil &&
        node.Spec.Validator.OperatorKeyring != nil &&
        node.Spec.Validator.OperatorKeyring.Secret != nil
}

func validateOperatorKeyringParams(node *seiv1alpha1.SeiNode) task.ValidateOperatorKeyringParams {
    s := node.Spec.Validator.OperatorKeyring.Secret
    return task.ValidateOperatorKeyringParams{
        SecretName:           s.SecretName,
        KeyName:              defaultStr(s.KeyName, "node_admin"),
        PassphraseSecretName: s.PassphraseSecretRef.SecretName,
        PassphraseSecretKey:  defaultStr(s.PassphraseSecretRef.Key, "passphrase"),
        Namespace:            node.Namespace,
    }
}
```

Insert into `buildBasePlan` (currently around lines 496-509) after the existing `validate-node-key` check:

```go
prog = append(prog, task.TaskTypeEnsureDataPVC)
if needsValidateSigningKey(node)          { prog = append(prog, task.TaskTypeValidateSigningKey) }
if needsValidateNodeKey(node)             { prog = append(prog, task.TaskTypeValidateNodeKey) }
if needsValidateOperatorKeyring(node)     { prog = append(prog, task.TaskTypeValidateOperatorKeyring) }  // <-- NEW
prog = append(prog, task.TaskTypeApplyStatefulSet, task.TaskTypeApplyService)
```

And the same insertion in `buildBootstrapPlan` (`bootstrap.go:53-64`).

Params factory at `planner.go:540-553` gets a new case:

```go
case task.TaskTypeValidateOperatorKeyring:
    return validateOperatorKeyringParams(node)
```

### Mount construction

In `internal/noderesource/noderesource.go`, add constants:

```go
operatorKeyringVolumeName = "operator-keyring"
operatorKeyringDirName    = "keyring-file" // matches sei-cosmos keyringFileDirName
keyringPassphraseEnvVar   = "SEI_KEYRING_PASSPHRASE"
```

Helper functions modeled on `signingKeyVolumes` / `signingKeyMounts`:

```go
func operatorKeyringVolumes(node *seiv1alpha1.SeiNode) []corev1.Volume {
    src := operatorKeyringSecretSource(node)
    if src == nil {
        return nil
    }
    return []corev1.Volume{{
        Name: operatorKeyringVolumeName,
        VolumeSource: corev1.VolumeSource{
            Secret: &corev1.SecretVolumeSource{
                SecretName:  src.SecretName,
                DefaultMode: ptr.To[int32](0o400),
            },
        },
    }}
}

func operatorKeyringMounts(node *seiv1alpha1.SeiNode) []corev1.VolumeMount {
    if operatorKeyringSecretSource(node) == nil {
        return nil
    }
    return []corev1.VolumeMount{{
        Name:      operatorKeyringVolumeName,
        MountPath: dataDir + "/" + operatorKeyringDirName,
        ReadOnly:  true,
    }}
}

// operatorKeyringEnvVars produces the env var that injects the keyring unlock
// passphrase into the sidecar container. The passphrase Secret is a separate
// resource from the keyring Secret (the latter is mounted as a volume).
func operatorKeyringEnvVars(node *seiv1alpha1.SeiNode) []corev1.EnvVar {
    src := operatorKeyringSecretSource(node)
    if src == nil {
        return nil
    }
    return []corev1.EnvVar{{
        Name: keyringPassphraseEnvVar,
        ValueFrom: &corev1.EnvVarSource{
            SecretKeyRef: &corev1.SecretKeySelector{
                LocalObjectReference: corev1.LocalObjectReference{Name: src.PassphraseSecretRef.SecretName},
                Key:                  defaultStr(src.PassphraseSecretRef.Key, "passphrase"),
            },
        },
    }}
}
```

Wire into pod spec — append `operatorKeyringVolumes(...)` to the pod's `Volumes`. **Critically:** append `operatorKeyringMounts(...)` and `operatorKeyringEnvVars(...)` to the **sidecar** container only (`buildSidecarContainer`), NEVER to the seid main container (`buildNodeMainContainer`) or the init container.

### Mount layout (sidecar container)

```
/sei/                                       (data PVC mount)
├── config/                                 (seid config; sidecar reads, not writes)
│   ├── genesis.json
│   ├── app.toml
│   └── config.toml
├── data/                                   (chain state, seid-managed)
└── keyring-file/                           (operator-keyring Secret, sidecar ONLY)
    ├── node_admin.info                     (encrypted protobuf-marshaled LocalInfo)
    └── <hex-of-bech32-bytes>.address       (name→address index)

ENV (sidecar process):
  SEI_KEYRING_BACKEND = "file"
  SEI_KEYRING_DIR     = "/sei/keyring-file"
  SEI_KEYRING_PASSPHRASE = "<from passphrase Secret>"   ← unset after init
```

The `keyring-file` directory name and the `.info`/`.address` suffixes are mandated by the Cosmos SDK file-keyring backend (`sei-cosmos/crypto/keyring/keyring.go:42`, `types.go:39-40`). The sidecar opens via `keyring.New(serviceName, BackendFile, "/sei", passphraseReader)`, which implicitly appends `keyring-file/` to the home dir.

### Bootstrap-pod isolation guard

Generalize `assertNoSigningKeyOnBootstrapPod` (currently in `internal/task/bootstrap_resources.go:333-348`) into `assertNoValidatorSecretsOnBootstrapPod`:

```go
// assertNoValidatorSecretsOnBootstrapPod fails closed if a future refactor
// accidentally lands ANY validator-owned Secret (signing key OR operator
// keyring OR operator-keyring passphrase) on the bootstrap pod-spec.
// Bootstrap pods run `seid start --halt-height` and must be physically
// incapable of signing — no signing-related material on their filesystem,
// no operator-account material in their env.
func assertNoValidatorSecretsOnBootstrapPod(node *seiv1alpha1.SeiNode, spec *corev1.PodSpec) error {
    if node.Spec.Validator == nil {
        return nil
    }
    type fb struct{ name, kind string }
    var forbidden []fb
    if sk := node.Spec.Validator.SigningKey; sk != nil && sk.Secret != nil {
        forbidden = append(forbidden, fb{sk.Secret.SecretName, "signing-key"})
    }
    if ok := node.Spec.Validator.OperatorKeyring; ok != nil && ok.Secret != nil {
        forbidden = append(forbidden, fb{ok.Secret.SecretName, "operator-keyring"})
        forbidden = append(forbidden, fb{ok.Secret.PassphraseSecretRef.SecretName, "operator-keyring-passphrase"})
    }
    // NodeKey deliberately excluded — node-key carries no signing authority;
    // bootstrap mounting it would be a design bug elsewhere, not a slashing risk.

    // Volumes
    for _, v := range spec.Volumes {
        if v.Secret == nil {
            continue
        }
        for _, f := range forbidden {
            if v.Secret.SecretName == f.name {
                return fmt.Errorf("bootstrap pod-spec for %s/%s references %s Secret %q on volume %q; "+
                    "bootstrap pods must never carry validator-owned credentials",
                    node.Namespace, node.Name, f.kind, f.name, v.Name)
            }
        }
    }

    // EnvVars — guard against passphrase leakage through env injection.
    for _, c := range spec.Containers {
        for _, ev := range c.Env {
            if ev.ValueFrom == nil || ev.ValueFrom.SecretKeyRef == nil {
                continue
            }
            for _, f := range forbidden {
                if ev.ValueFrom.SecretKeyRef.Name == f.name {
                    return fmt.Errorf("bootstrap pod-spec for %s/%s references %s Secret %q in container %q env; "+
                        "bootstrap pods must never carry validator-owned credentials",
                        node.Namespace, node.Name, f.kind, f.name, c.Name)
                }
            }
        }
    }

    return nil
}
```

Update the single existing caller. New file `internal/task/bootstrap_resources_test.go` (or extend the existing test) to cover: signing-key volume rejection, operator-keyring volume rejection, passphrase env rejection.

### RBAC

**Existing coverage:** `+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch` on `internal/controller/node/controller.go:63` is sufficient for reading both the keyring Secret and the passphrase Secret. **No new permission needed for Phase 1.**

**Phase 4 addition** (TokenReview, when #165 lands): add the marker

```go
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
```

alongside the existing block (`internal/controller/node/controller.go:53-63`). Run `make manifests` to regenerate. The new rule appears in `manifests/role.yaml` automatically.

**Open question:** the sidecar currently runs under `p.ServiceAccount` (the platform-wide SA from `internal/platform/platform.go:24`). For tighter blast radius on TokenReview privilege, a follow-up workstream should mint a dedicated `sei-sidecar` SA with the narrow `tokenreviews:create` grant only. Out of scope for v1; documented as a Phase 4 follow-up.

### Container security context (recommended hardening — ships with Phase 1)

The current sidecar container has no `SecurityContext` set at all. Tighten in `buildSidecarContainer` as part of this workstream:

```go
c.SecurityContext = &corev1.SecurityContext{
    RunAsNonRoot:             ptr.To(true),
    RunAsUser:                ptr.To[int64](65532), // nonroot UID in distroless/static-debian12
    RunAsGroup:               ptr.To[int64](65532),
    AllowPrivilegeEscalation: ptr.To(false),
    ReadOnlyRootFilesystem:   ptr.To(true),
    Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
    SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
}

// Pod-level: required so the non-root sidecar UID can read the
// Secret-projected keyring files (which kubelet owns root:root by default).
podSpec.SecurityContext = &corev1.PodSecurityContext{
    FSGroup: ptr.To[int64](65532),
}
```

Notes:
- `ReadOnlyRootFilesystem: true` is safe — the sidecar writes only to `sidecar.db` (SQLite) under `homeDir=/sei`, which is the PVC mount (writable).
- `automountServiceAccountToken` is currently undeclared (kubelet default `true`). **Must stay true** for Phase 4 TokenReview to work; document the dependency in a code comment.
- Do NOT extend this hardening to the seid main container in this workstream — that's a separate, larger blast-radius change.

---

## Component B — Sidecar keyring backend (seictl)

### Env contract

Three new envs in `serve.go` (extend the existing block around lines 42-58):

| Env | Values | Default | Required when | Notes |
|---|---|---|---|---|
| `SEI_KEYRING_BACKEND` | `file` \| `test` \| `os` | unset (governance disabled) | governance signing in use | Unknown value → fail-fast at startup |
| `SEI_KEYRING_DIR` | abs path | `$SEI_HOME/keyring-file` | `backend == file` | Read-only access; sidecar never writes |
| `SEI_KEYRING_PASSPHRASE` | string | unset | `backend == file` | Wiped from process env after init |

Phase-1 default (no governance signing): `SEI_KEYRING_BACKEND` unset. The sidecar starts normally and rejects sign-tx tasks with `keyring not configured`.

Phase-2 default (governance signing on): controller injects `SEI_KEYRING_BACKEND=file`, `SEI_KEYRING_DIR=/sei/keyring-file`, `SEI_KEYRING_PASSPHRASE` via `valueFrom.secretKeyRef` (see Component A's `operatorKeyringEnvVars`).

### Code sketch (additions to `serve.go`)

```go
keyringBackend := os.Getenv("SEI_KEYRING_BACKEND")
keyringDir := os.Getenv("SEI_KEYRING_DIR")
keyringPassphrase := os.Getenv("SEI_KEYRING_PASSPHRASE")

var kr keyring.Keyring
if keyringBackend != "" {
    switch keyringBackend {
    case "file":
        if keyringDir == "" {
            keyringDir = filepath.Join(homeDir, "keyring-file")
        }
        if keyringPassphrase == "" {
            return fmt.Errorf("SEI_KEYRING_PASSPHRASE required when SEI_KEYRING_BACKEND=file")
        }
    case "test", "os":
        // permitted as configured
    default:
        return fmt.Errorf("unsupported SEI_KEYRING_BACKEND %q (allowed: file|test|os)", keyringBackend)
    }
    var err error
    kr, err = openKeyring(keyringBackend, keyringDir, keyringPassphrase)
    if err != nil {
        // redact passphrase from error chain
        return fmt.Errorf("keyring open failed: %w", redactPassphrase(err, keyringPassphrase))
    }
    _ = os.Unsetenv("SEI_KEYRING_PASSPHRASE")

    seilog.Info("keyring opened",
        "backend", keyringBackend, "dir", keyringDir)
}

// Pass kr to engine handlers via TaskExecutionConfig (new field):
engine.WithKeyring(kr)
```

Where `openKeyring` is:

```go
func openKeyring(backend, dir, passphrase string) (keyring.Keyring, error) {
    var input io.Reader = nil
    if backend == keyring.BackendFile {
        // 99designs/keyring (wrapped by sei-cosmos) asks for the passphrase
        // twice on some code paths. Provide it twice; the SDK closes the reader.
        input = strings.NewReader(passphrase + "\n" + passphrase + "\n")
    }
    return keyring.New(
        sdk.KeyringServiceName(),
        backend,
        filepath.Dir(dir), // KeyringServiceName uses dir + "/keyring-file"
        input,
        codec.NewProtoCodec(types.NewInterfaceRegistry()), // share with txCfg
    )
}
```

### Startup smoke test

After `openKeyring` succeeds, walk the configured key names (initially just `node_admin`) and confirm each can be resolved:

```go
for _, keyName := range []string{"node_admin"} {
    if _, err := kr.Key(keyName); err != nil {
        return fmt.Errorf("keyring smoke test: key %q not found: %w", keyName, err)
    }
}
```

In Phase 2, the controller injects an additional env `SEI_KEYRING_REQUIRED_KEYS=node_admin` and the sidecar reads/splits/iterates. Failure → process exit non-zero → `CrashLoopBackOff` → operator pages.

Bounded retry inside the smoke test (3 attempts, 2s backoff) absorbs the rare kubelet Secret-mount race where the file is briefly absent. Beyond that, hard fail.

### Genesis-path isolation

`sidecar/tasks/generate_gentx.go` today hardcodes `keyring.BackendTest`. Refactor to:
1. Accept the keyring from the shared factory (passed via `TaskExecutionConfig`)
2. **Override to `BackendTest`** within the gentx handler regardless of the env-configured backend

Genesis ceremonies must remain isolated from production keyring config — the gentx-generated key is throwaway by design.

### Documentation

Add `sidecar/docs/keyring.md` covering: env contract, backends supported, passphrase handling, smoke-test semantics, operator runbook for creating the Secrets.

---

## Component C — Sign-tx task family (seictl sidecar)

### Task type registration

Add to `sidecar/engine/types.go`:

```go
const (
    // ... existing task types ...
    TaskTypeGovVote            TaskType = "gov-vote"
    TaskTypeGovSubmitProposal  TaskType = "gov-submit-proposal"
    TaskTypeGovDeposit         TaskType = "gov-deposit"
)
```

### Param schemas

```go
type GovVoteParams struct {
    ChainID    string `json:"chainId"`       // required; must match SEI_CHAIN_ID
    KeyName    string `json:"keyName"`       // default "node_admin"
    ProposalID uint64 `json:"proposalId"`
    Option     string `json:"option"`        // "yes"|"no"|"abstain"|"no_with_veto"
    Fees       string `json:"fees"`          // coin string, denominated in usei; default "4000usei"
    Gas        uint64 `json:"gas"`           // default 200000
    Memo       string `json:"memo,omitempty"`
}

type GovSubmitProposalParams struct {
    ChainID         string `json:"chainId"`
    KeyName         string `json:"keyName"`
    ProposalType    string `json:"proposalType"`     // "software-upgrade" only in v1
    UpgradeName     string `json:"upgradeName"`      // e.g. "v6.0.0"
    UpgradeHeight   int64  `json:"upgradeHeight"`    // > 0
    UpgradeInfo     string `json:"upgradeInfo,omitempty"`
    Title           string `json:"title"`
    Description     string `json:"description"`
    Deposit         string `json:"deposit"`          // coin string, e.g. "10000000usei"
    IsExpedited     bool   `json:"isExpedited,omitempty"`
    Fees            string `json:"fees"`             // default "10000usei"
    Gas             uint64 `json:"gas"`              // default 500000
    Memo            string `json:"memo,omitempty"`
}

type GovDepositParams struct {
    ChainID    string `json:"chainId"`
    KeyName    string `json:"keyName"`
    ProposalID uint64 `json:"proposalId"`
    Amount     string `json:"amount"`       // coin string
    Fees       string `json:"fees"`         // default "5000usei"
    Gas        uint64 `json:"gas"`          // default 250000
    Memo       string `json:"memo,omitempty"`
}
```

### Shared signing helper

New file `sidecar/tasks/sign_and_broadcast.go`:

```go
type SignAndBroadcastInput struct {
    ChainID  string
    KeyName  string
    Msg      sdk.Msg
    Fees     string
    Gas      uint64
    Memo     string
    TaskID   string  // for tx-hash persistence keyed by task
}

type SignAndBroadcastResult struct {
    TxHash        string
    Height        int64
    Code          uint32
    Codespace     string
    RawLog        string
    GasWanted     int64
    GasUsed       int64
    Sequence      uint64
    AccountNumber uint64
    ChainID       string
    BroadcastedAt time.Time
    IncludedAt    *time.Time
}

func SignAndBroadcast(ctx context.Context, cfg ExecutionConfig, in SignAndBroadcastInput) (*SignAndBroadcastResult, error) {
    // 1. Chain-id guard
    nodeStatus, err := cfg.RPC.Get(ctx, "/status")
    if err != nil { return nil, fmt.Errorf("rpc /status: %w", err) }
    if nodeStatus.NodeInfo.Network != in.ChainID {
        return nil, Terminal(fmt.Errorf("chain mismatch: params.chainId=%q node.network=%q", in.ChainID, nodeStatus.NodeInfo.Network))
    }
    if in.ChainID != os.Getenv("SEI_CHAIN_ID") {
        return nil, Terminal(fmt.Errorf("chain mismatch: params.chainId=%q sidecar.SEI_CHAIN_ID=%q", in.ChainID, os.Getenv("SEI_CHAIN_ID")))
    }

    // 2. Open keyring entry
    kr := cfg.Keyring
    info, err := kr.Key(in.KeyName)
    if err != nil { return nil, Terminal(fmt.Errorf("keyring key %q: %w", in.KeyName, err)) }
    fromAddr := info.GetAddress()

    // 3. Fetch fresh sequence + account number (NEVER cache across retries)
    clientCtx := buildClientContext(cfg, in.ChainID, kr, fromAddr, in.KeyName)
    accNum, seq, err := authtypes.AccountRetriever{}.GetAccountNumberSequence(clientCtx, fromAddr)
    if err != nil { return nil, fmt.Errorf("account retrieve: %w", err) }

    // 4. Build tx
    f := tx.Factory{}.
        WithChainID(in.ChainID).
        WithKeybase(kr).
        WithTxConfig(clientCtx.TxConfig).
        WithAccountRetriever(authtypes.AccountRetriever{}).
        WithAccountNumber(accNum).
        WithSequence(seq).
        WithGas(in.Gas).
        WithFees(in.Fees).
        WithMemo(in.Memo).
        WithSignMode(signingtypes.SignMode_SIGN_MODE_DIRECT)

    unsignedTx, err := tx.BuildUnsignedTx(f, in.Msg)
    if err != nil { return nil, fmt.Errorf("build unsigned tx: %w", err) }
    if err := tx.Sign(f, in.KeyName, unsignedTx, true); err != nil {
        return nil, fmt.Errorf("sign tx: %w", err)
    }
    txBytes, err := clientCtx.TxConfig.TxEncoder()(unsignedTx.GetTx())
    if err != nil { return nil, fmt.Errorf("encode tx: %w", err) }

    // 5. Idempotency: compute deterministic tx hash, persist BEFORE broadcast
    txHash := strings.ToUpper(hex.EncodeToString(tmhash.Sum(txBytes)))
    if err := cfg.Store.PersistTxHashCheckpoint(in.TaskID, txHash, seq, accNum, in.ChainID); err != nil {
        return nil, fmt.Errorf("persist hash checkpoint: %w", err)
    }

    // 6. On retry-after-broadcast: if we have a persisted hash and the chain has it, return success
    if existing, err := cfg.RPC.QueryTxByHash(ctx, txHash); err == nil && existing != nil {
        return resultFromTxResponse(existing, accNum, seq, in.ChainID), nil
    }

    // 7. Broadcast (sync mode — CheckTx feedback, no indeterminate block-mode hang)
    resp, err := clientCtx.BroadcastTxSync(txBytes)
    if err != nil { return nil, fmt.Errorf("broadcast: %w", err) }
    if resp.Code != 0 {
        // CheckTx-level rejection: terminal
        return nil, Terminal(fmt.Errorf("checkTx failed: code=%d codespace=%s log=%s",
            resp.Code, resp.Codespace, resp.RawLog))
    }

    // 8. Poll for inclusion (existing sidecar/rpc/client.go covers GET-shaped RPC)
    included, err := cfg.RPC.PollTxByHash(ctx, txHash, 60*time.Second)
    if err != nil { return nil, fmt.Errorf("await inclusion: %w", err) }

    return resultFromTxResponse(included, accNum, seq, in.ChainID), nil
}
```

### Per-Msg handlers

Each handler constructs the typed Msg and delegates to `SignAndBroadcast`. Example for `gov-vote`:

```go
func (e *govVoteExecution) Execute(ctx context.Context) error {
    p := e.params
    optionEnum, err := govtypes.VoteOptionFromString(govutils.NormalizeVoteOption(p.Option))
    if err != nil {
        return Terminal(fmt.Errorf("invalid vote option %q: %w", p.Option, err))
    }
    info, err := e.cfg.Keyring.Key(p.KeyName)
    if err != nil { return Terminal(err) }
    msg := govtypes.NewMsgVote(info.GetAddress(), p.ProposalID, optionEnum)

    result, err := SignAndBroadcast(ctx, e.cfg, SignAndBroadcastInput{
        ChainID: p.ChainID, KeyName: p.KeyName, Msg: msg,
        Fees: defaultStr(p.Fees, "4000usei"),
        Gas:  defaultUint(p.Gas, 200_000),
        Memo: defaultStr(p.Memo, "seictl-sidecar"),
        TaskID: e.id,
    })
    if err != nil { return err }
    e.result = result
    e.complete()
    return nil
}
```

### Software-upgrade proposal specifics

**Important:** Sei runs Cosmos SDK gov v1beta1 (not v1). `MsgSubmitProposal` wraps a `Content`, not a `[]sdk.Msg` slice. `SoftwareUpgradeProposal` is a `Content`, not a `Msg`. There is no `MsgSoftwareUpgrade` in this SDK version.

```go
import (
    govtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/gov/types"
    upgradetypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/upgrade/types"
)

plan := upgradetypes.Plan{
    Name:   p.UpgradeName,    // e.g. "v6.0.0"
    Height: p.UpgradeHeight,  // > 0; Time field is DEPRECATED, leave zero
    Info:   p.UpgradeInfo,    // optional URL or JSON
}
content := upgradetypes.NewSoftwareUpgradeProposal(p.Title, p.Description, plan)

initDeposit, err := sdk.ParseCoinsNormalized(p.Deposit) // e.g. "10000000usei"
if err != nil { return Terminal(fmt.Errorf("parse deposit %q: %w", p.Deposit, err)) }

msg, err := govtypes.NewMsgSubmitProposalWithExpedite(content, initDeposit, fromAddr, p.IsExpedited)
if err != nil { return Terminal(fmt.Errorf("construct MsgSubmitProposal: %w", err)) }
```

Field mapping from seienv's `propose.sh` template:

| seienv arg | Maps to |
|---|---|
| positional `<name>` | `Plan.Name` |
| `--upgrade-height <h>` | `Plan.Height` |
| `--upgrade-info <i>` | `Plan.Info` |
| `--title <t>` | `SoftwareUpgradeProposal.Title` |
| `--description <d>` | `SoftwareUpgradeProposal.Description` |
| `--deposit <amt>` | `MsgSubmitProposal.InitialDeposit` |
| `--from <key>` | `MsgSubmitProposal.Proposer` (resolved via keyring) |
| `--upgrade-time` | **DO NOT SET** — deprecated |

### Idempotency contract (load-bearing)

The engine deduplicates by caller-supplied UUID at task submission (`engine.go:91-138`). The handler must additionally protect against the case where: tx was signed and broadcast, sidecar crashed before persisting result, engine restarts and re-enters Execute.

**Solution:** persist `{taskID → txHash}` to the engine's store **before** `BroadcastTxSync`. On retry-after-broadcast:

1. Look up the persisted `txHash` for this `taskID`
2. Query the local node for that tx hash
3. If found → success; populate result from chain
4. If not found AND the account sequence has not advanced beyond `persistedSequence` → safe to re-sign and broadcast
5. If not found AND the sequence has advanced → tx may have been DeliverTx-rejected; surface terminal error

This composes cleanly with the existing marker-file pattern in `generate_gentx.go:80,112`.

### Chain-confusion guard

Refuse to sign if **any** of:
- `params.chainId != os.Getenv("SEI_CHAIN_ID")`
- `params.chainId != /status.NodeInfo.Network` (the actual chain the local seid is on)

Check **before** opening the keyring (i.e. before any operation that needs the passphrase). Slashing-equivalent risk is low (the signature is valid only on the named chain; rejected at broadcast for mismatched chain) but the guard prevents a class of confusing operational errors AND ensures we never expose a signature for a chain we didn't intend to sign on.

### Default gas/fees

Sei uses **usei** denomination (1 SEI = 1,000,000 usei). Min-gas-prices default: `0.01usei` (`cmd/seid/cmd/root.go:409`). seienv's `vote.go:15` uses `--fees 20sei` which is a latent bug — `seid` either fails to parse or accepts a non-existent `sei` denom. **When porting, normalize to `usei` and reject inputs whose denom is not `usei`.**

| Msg | Default gas | Default fee (at 0.02usei) |
|---|---|---|
| `MsgVote` | 200,000 | `4000usei` |
| `MsgDeposit` | 250,000 | `5000usei` |
| `MsgSubmitProposal` (SoftwareUpgrade) | 500,000 | `10000usei` |

Operators may override via task params. Reject (Terminal) any non-`usei` denom in `Fees` and `Deposit`.

### Sign mode and codec

Sei's default sign mode is **`SIGN_MODE_DIRECT`** (proto bytes). `DefaultSignModes` at `sei-cosmos/x/auth/tx/mode_handler.go:11-14` lists `[DIRECT, LEGACY_AMINO_JSON]`; the first is the default. All gov messages should sign with DIRECT. Set explicitly: `f.WithSignMode(signingtypes.SignMode_SIGN_MODE_DIRECT)`.

The codec is shared with `generate_gentx.go`'s pattern (`makeCodec()` returns a `Codec` + `TxConfig`). Extend the gentx codec to register `govtypes.RegisterInterfaces` and `upgradetypes.RegisterInterfaces` so the sign-tx handlers share one codec.

### Task result schema

```json
{
  "txHash":        "B7E2...",                  // hex, 64 chars
  "code":          0,                          // 0=success
  "codespace":     "",                         // non-empty when code != 0
  "rawLog":        "[{...events...}]",
  "height":        1234567,
  "gasWanted":     200000,
  "gasUsed":       142318,
  "sequence":      42,
  "accountNumber": 17,
  "chainId":       "pacific-1",
  "msgType":       "/cosmos.gov.v1beta1.MsgVote",
  "broadcastedAt": "2026-05-11T...",
  "includedAt":    "2026-05-11T...",
  // task-type-specific extension:
  "proposalId":    123                         // for gov-vote and gov-deposit
}
```

Minimum required: `{txHash, code, height, rawLog}`. The rest are operator-debug-grade and cheap to include.

### OpenAPI schema update

Update `sidecar/api/openapi.yaml`:
1. Add `gov-vote`, `gov-submit-proposal`, `gov-deposit` to the task type enum
2. Add per-type param schema components
3. Add the result schema under the task result component
4. Add a `securitySchemes: bearerAuth` declaration (Phase 4 will wire it; declare now for forward-compat)
5. Add a top-level note: "Sign-tx tasks ship unauthenticated in Phase 1-3; see issue #165 for the authn enablement plan."

### Client SDK helpers

Add to `sidecar/client/tasks.go`:

```go
func (c *SidecarClient) SubmitGovVoteTask(ctx context.Context, taskID string, p GovVoteParams) (*Task, error)
func (c *SidecarClient) SubmitGovSubmitProposalTask(ctx context.Context, taskID string, p GovSubmitProposalParams) (*Task, error)
func (c *SidecarClient) SubmitGovDepositTask(ctx context.Context, taskID string, p GovDepositParams) (*Task, error)
```

Each thin wrapper around `SubmitTask` with the right type and params.

### Pre-authn warning notice

Until Phase 4 authn lands, each new handler file carries a top-of-file comment:

```go
// SECURITY POSTURE NOTE — sei-protocol/seictl#163 / #165
//
// This handler accepts sign-and-broadcast requests over the sidecar's HTTP
// API, which is unauthenticated in Phase 1-3 of the governance-flow workstream.
// Any caller with network reach to port 7777 can submit governance txs as
// the validator's operator account. This is comparable to the seienv+SSH
// status-quo trust scope (anyone with the SSH key has equivalent power)
// but the K8s network blast radius is wider.
//
// Phase 4 (#165) installs TokenReview-based authn on mutating endpoints.
// REMOVE THIS NOTICE when #165 lands.
```

The README and the OpenAPI spec carry equivalent notices.

---

## Component D — seictl `gov` CLI subcommands (seictl)

### Command structure

New cobra command tree under `cmd/gov/`:

```
seictl gov vote <proposal-id> <yes|no|abstain|no_with_veto>
              --validator <SeiNode-name> [--namespace <ns>]
              [--fees <fees>] [--memo <memo>] [--task-id <uuid>]
              [--wait] [--timeout <duration>]

seictl gov submit-proposal software-upgrade
              --validator <SeiNode-name> [--namespace <ns>]
              --upgrade-name <name> --upgrade-height <h>
              [--upgrade-info <url>] --title <t> --description <d>
              --deposit <amount> [--is-expedited]
              [--fees <fees>] [--memo <memo>] [--task-id <uuid>]
              [--wait] [--timeout <duration>]

seictl gov deposit <proposal-id> <amount>
              --validator <SeiNode-name> [--namespace <ns>]
              [--fees <fees>] [--memo <memo>] [--task-id <uuid>]
              [--wait] [--timeout <duration>]
```

Fleet fan-out across a SeiNodeDeployment's validators:

```
seictl gov vote 5 yes --deployment <SNDeployment-name> [--threads N]
```

### Sidecar discovery

```go
// Pseudo-code
func resolveSidecarURL(ctx context.Context, kc kubernetes.Interface, dyn dynamic.Interface, name, ns string) (string, error) {
    obj, err := dyn.Resource(seinodeGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
    if err != nil { return "", err }
    // Resolve headless Service DNS for this SeiNode's StatefulSet pod-0:
    //   <name>-0.<name>.<ns>.svc.cluster.local:7777
    return fmt.Sprintf("http://%s-0.%s.%s.svc.cluster.local:7777", name, name, ns), nil
}
```

Operators running seictl from outside the cluster can override via `--sidecar-url` or use `kubectl port-forward` separately. The default in-cluster pattern is the recommended path.

### Authentication (Phase 4)

Stub for Phase 4: when `SEI_AUTHZ_ENABLED=true` is observed on the target sidecar (via `GET /v0/status`), the CLI mints a token:

```go
tr, err := kc.CoreV1().ServiceAccounts(ns).CreateToken(ctx, saName, &authnv1.TokenRequest{}, metav1.CreateOptions{})
if err != nil { return err }
req.Header.Set("Authorization", "Bearer "+tr.Status.Token)
```

Until Phase 4 ships, the CLI skips this step. The sidecar's `SEI_AUTHZ_ENABLED` flag controls whether the auth check is enforced server-side, so the CLI can be auth-aware ahead of time without breaking Phase 1-3 deployments.

### Output

Default `--wait=true`: poll `GET /v0/tasks/{id}` until terminal, render result:

```
$ seictl gov vote 5 yes --validator my-validator
Submitted task abc-123 to my-validator-0...:7777
Waiting for inclusion ...
✓ tx 0xB7E2... included at height 1234567 (code=0, gasUsed=142318)
```

Fan-out fan-in renders a table:

```
$ seictl gov vote 5 yes --deployment my-validators
VALIDATOR         TASK ID    STATUS     TX HASH    HEIGHT    LATENCY
val-0             abc-123    success    0xB7E2...  1234567   2.3s
val-1             abc-124    success    0xA9C1...  1234567   2.1s
val-2             abc-125    failed     -          -         -        (chainId mismatch)
```

Non-zero exit on any failure (operator triages from the table).

### Idempotent retry

`--task-id <uuid>` lets an operator retry safely: same UUID → engine dedupe → same outcome. If absent, the CLI generates and prints the UUID so a retry is trivial.

### Documentation

New `docs/gov.md` mirroring the structure of `slanders/seienv/cheatsheet.md`'s governance section. Cross-reference from `docs/design/in-pod-governance-signing.md` (this file) and the new design's own README pointer.

---

## Component E — Authn + authz middleware (seictl sidecar, Phase 4)

### Middleware shape

New file `sidecar/server/auth.go`. Wraps the existing mux in `NewServer`:

```go
type authMiddleware struct {
    inner   http.Handler
    client  kubernetes.Interface
    allowed map[string]struct{}
    log     *seilog.Logger
}

var publicPaths = map[string]struct{}{
    "/v0/healthz": {}, "/v0/livez": {}, "/v0/metrics": {},
}

func (a *authMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    if _, public := publicPaths[r.URL.Path]; public {
        a.inner.ServeHTTP(w, r)
        return
    }
    // Read-only task endpoints (GET /v0/tasks*) are unauthenticated.
    if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v0/tasks") {
        a.inner.ServeHTTP(w, r)
        return
    }

    reqID := uuid.NewString()
    start := time.Now()
    tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
    if tok == "" || tok == r.Header.Get("Authorization") {
        a.audit(reqID, "", r, http.StatusUnauthorized, start)
        writeError(w, http.StatusUnauthorized, "bearer token required")
        return
    }
    tr, err := a.client.AuthenticationV1().TokenReviews().Create(r.Context(),
        &authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: tok}}, metav1.CreateOptions{})
    if err != nil || !tr.Status.Authenticated {
        a.audit(reqID, "", r, http.StatusUnauthorized, start)
        writeError(w, http.StatusUnauthorized, "token review failed")
        return
    }
    caller := tr.Status.User.Username
    if _, ok := a.allowed[caller]; !ok {
        a.audit(reqID, caller, r, http.StatusForbidden, start)
        writeError(w, http.StatusForbidden, "caller not permitted")
        return
    }
    r = r.WithContext(context.WithValue(r.Context(), callerCtxKey{}, caller))
    rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
    a.inner.ServeHTTP(rw, r)
    a.audit(reqID, caller, r, rw.status, start)
}
```

Wire in `NewServer`:

```go
var handler http.Handler = mux
if os.Getenv("SEI_AUTHZ_ENABLED") == "true" {
    handler = newAuthMiddleware(mux, kubeClient, parseAllowedCallers(os.Getenv("SEI_AUTHZ_ALLOWED_CALLERS")), log)
}
return &http.Server{Handler: handler, ...}
```

### TokenReview client

`kubernetes.NewForConfig(rest.InClusterConfig())` — pure Go, no CGO, distroless-compatible. Reads SA token from `/var/run/secrets/kubernetes.io/serviceaccount/token`. The sidecar's pod must have `automountServiceAccountToken: true` (currently kubelet default; will be made explicit in Component A's hardening).

Cache TokenReview results for 30s keyed on a SHA-256 of the bearer token to bound K8s API server load.

### Allowlist

`SEI_AUTHZ_ALLOWED_CALLERS` — comma-separated list of caller identities:

```
system:serviceaccount:sei-k8s-controller-system:sei-k8s-controller-manager,
system:serviceaccount:operator-tools:gov-operator
```

Empty allowlist = fail-closed (deny all mutations).

Authz model is intentionally coarse-grained in v1: any allowed caller can submit any task type. Per-task-type policy is a v1.1 conversation.

### Audit logging

Every authenticated request emits a structured `seilog` line:

```json
{
  "msg":         "sidecar.api.mutation",
  "event":       "sidecar.api.mutation",
  "caller":      "system:serviceaccount:sei-k8s-controller-system:sei-k8s-controller-manager",
  "method":      "POST",
  "path":        "/v0/tasks",
  "request_id":  "abc-123-...",
  "result":      200,
  "latency_ms":  47,
  "task_type":   "gov-vote"
}
```

`task_type` is added in a second log line emitted after the task type is parsed from the request body. For Phase 4 v1, two log lines per mutating request (one at auth-time with no task_type, one at handler-dispatch with task_type) is acceptable; collapse to one line in a follow-up.

### Phased rollout

1. Phase 4 lands the middleware with `SEI_AUTHZ_ENABLED=false` default; existing controller flows are unaffected.
2. Operators upgrade their controller manifest to inject `SEI_AUTHZ_ENABLED=true` + `SEI_AUTHZ_ALLOWED_CALLERS=<controller-SA>` on the sidecar container.
3. Phase 5 (separate work): controller-side seictl-CLI updates to mint and present tokens by default.
4. Remove the pre-authn warning notices from Component C's handlers and the README.

### RBAC for the sidecar SA

Single new permission via kubebuilder marker on the controller:

```go
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
```

Run `make manifests` to regenerate `manifests/role.yaml`. Follow-up workstream: split the sidecar's SA from the controller-manager's SA for blast-radius isolation.

---

## Security posture

### Trust boundary summary

| Component | Has signing key? | Has node key? | Has operator keyring? |
|---|---|---|---|
| Operator laptop | No (Phase 4: bearer token only) | No | No |
| seid main container | **Yes** (consensus) | **Yes** (P2P) | No |
| seictl sidecar | No | No | **Yes** (governance) |
| Bootstrap pod | No | No | No |

Key separation rationale:
- A compromised seid binary signs blocks (consensus key) but cannot vote on proposals (no operator keyring).
- A compromised sidecar can vote/propose but cannot sign blocks or impersonate the P2P peer.
- A compromised bootstrap pod runs `seid start --halt-height` with no on-chain identity; cannot sign anything.

This is materially stronger than seienv's "operator account key on every EC2 host that runs seid" model.

### SSH-to-K8s trust scope comparison

| Aspect | seienv (today) | sidecar (Phase 1-3) | sidecar (Phase 4) |
|---|---|---|---|
| Who can sign | Anyone with the SSH key | Anyone with network reach to pod:7777 | Anyone with a bearer token whose identity is in the allowlist |
| Network blast radius | Reachable from operator subnet | Reachable from any in-cluster pod | Authenticated identity required; cluster network reach is necessary but not sufficient |
| Key custody | Operator laptop SSH key + EC2 host keyring | Pod-mounted Secret only | Same |
| Caller attribution | SSH user (`ubuntu`); shared key | None (anonymous) | TokenReview-validated identity |
| Audit trail | EC2 `auth.log` + `seid` stdout on host | sidecar engine task store (no caller) | Structured audit log per mutating request |

**Phase 1-3 ships with comparable trust scope to today** (cluster network reach instead of SSH key). Phase 4 materially improves on the baseline.

### Container security context (re-stated)

Tightened in Component A as part of Phase 1:

- `runAsNonRoot: true` + `runAsUser: 65532`
- `readOnlyRootFilesystem: true`
- `allowPrivilegeEscalation: false`
- `capabilities.drop: [ALL]`
- `seccompProfile: RuntimeDefault`
- Pod-level `fsGroup: 65532` (required so non-root sidecar can read 0o400 Secret mounts)
- `automountServiceAccountToken: true` (kubelet default; documented dependency)

### Slashing risk

Governance-tx signing is **not** double-sign-able in the consensus sense — voting twice on the same proposal is rejected by the chain (proposer-vote uniqueness), not slashed. The chain-confusion guard prevents cross-chain signature exposure. The chief remaining operational risk is "submit-proposal with wrong upgrade height" which is a manual-input bug, not a protocol-level slashing event.

---

## Failure modes

| Failure | Detection | Response |
|---|---|---|
| Keyring volume mount missing | startup smoke test `kr.Key(name)` returns `keyring.ErrKeyNotFound` | Process exit non-zero → `CrashLoopBackOff` |
| Passphrase wrong | `keyring.New` returns "incorrect passphrase" | Process exit; log redacted error |
| `keyName` absent from keyring | smoke test resolves to error | Process exit |
| Sign-tx for unknown key name | task handler returns `Terminal` | Task `failed`; `code=4xx-shape`; controller surfaces in SeiNode `.status` |
| Chain RPC unreachable during broadcast | RPC client error | Task transient; engine retries via same-UUID resubmit |
| Account sequence mismatch | chain returns `account sequence mismatch` | Task `failed` with chain error verbatim; operator triages |
| Insufficient fees | chain error | Task `failed`; operator increases `--fees` and retries (new UUID OK) |
| Chain-confusion guard hits | handler `Terminal` before any keyring access | Task `failed`; no signature exposed |
| Tx broadcast OK, inclusion poll timeout | result includes `code=0, height=0, includedAt=null` | CLI exits non-zero with explicit "broadcast succeeded but not yet observed in chain" message; operator queries by tx hash directly |
| Sidecar crash post-broadcast pre-result | engine restart re-enters handler; persisted tx hash query returns inclusion | Task completes idempotently with chain-observed result |
| Sidecar crash pre-broadcast post-sign | engine restart re-enters handler; no persisted hash → re-sign with fresh sequence | New tx broadcast; sequence advance prevents replay |

---

## Rotation procedures

### Passphrase rotation

The CRD makes both Secret references immutable but does not constrain the Secret **contents**. To rotate the passphrase:

1. Operator commits new SOPS-encrypted passphrase Secret (re-encrypts keyring with new passphrase locally first).
2. GitOps applies; kubelet's projected-Secret resync eventually refreshes the mount.
3. **The running sidecar holds the old unlocked keyring in process memory.** It will not pick up the change. `kubectl delete pod <validator-sts-0>` to force a restart; StatefulSet reschedules; new sidecar runs the smoke test with the new passphrase.

Hot rotation is NOT supported in v1.

### Key rotation (different operator account)

Rotating to a different key name within the same Secret: no controller change; operator updates the Secret to add the new key, adjusts `spec.validator.operatorKeyring.secret.keyName`, applies. Wait — `keyName` is currently not CEL-immutable; this is the cleanest mutability surface for rotation.

Rotating to a different Secret entirely (different namespace, different name): `secretName` is CEL-immutable; force `kubectl delete --cascade=orphan` on the SeiNode and re-apply. This is intentional — the operator-account key change is a privileged operation.

### Lost passphrase

The encrypted keyring is unrecoverable without the passphrase. Operator must:
1. Generate a new operator account locally (`seid keys add`)
2. Update on-chain validator records (`MsgEditValidator` requires the OLD operator key — if truly lost, the validator's operator account is unrecoverable; operator must coordinate via on-chain governance for any consensus-key-only flows)

This is the same constraint as any Cosmos validator operator-account loss; the sidecar architecture does not change the recovery story.

---

## Operator runbook: creating the Secrets

```bash
# 1. Generate operator-account key locally on a trusted workstation.
mkdir -p /tmp/seictl-keyring
seid keys add node_admin \
    --keyring-backend file \
    --keyring-dir /tmp/seictl-keyring
# Prompts for passphrase. Note: the file backend stores the passphrase
# nowhere on disk — you must remember it and supply it to the sidecar via
# the passphrase Secret below.

# 2. Verify shape.
ls /tmp/seictl-keyring/keyring-file/
#   node_admin.info
#   abc123....address

# 3. Build keyring Secret. --from-file=<dir> projects each file under its
#    basename as a data key — exactly what the controller mounts back as
#    /sei/keyring-file/.
kubectl create secret generic validator-gov-keyring \
    --namespace=sei-validators \
    --from-file=/tmp/seictl-keyring/keyring-file/

# 4. Build passphrase Secret (separate from keyring).
kubectl create secret generic validator-gov-passphrase \
    --namespace=sei-validators \
    --from-literal=passphrase='<your-passphrase>'

# 5. Reference both from SeiNode spec:
#    spec:
#      validator:
#        operatorKeyring:
#          secret:
#            secretName: validator-gov-keyring
#            keyName: node_admin
#            passphraseSecretRef:
#              secretName: validator-gov-passphrase
#              key: passphrase

# 6. Verify the controller acknowledges the keyring:
kubectl get seinode <name> -o yaml | yq '.status.conditions[] | select(.type == "OperatorKeyringReady")'
# Expect: status: "True", reason: "OperatorKeyringValidated"

# 7. Wipe local material.
shred -u /tmp/seictl-keyring/keyring-file/*
rmdir /tmp/seictl-keyring/keyring-file /tmp/seictl-keyring
```

For GitOps via SOPS: same Secret YAML, `sops -e -i` before commit, Flux/ArgoCD decrypts on apply. ESO operators store the keyring directory contents + passphrase in AWS Secrets Manager and project via `ExternalSecret` resources with the same final shape.

---

## Implementation sequencing

Phase 1 (parallel — independent work, ~2 weeks combined):
- **#219** (sei-k8s-controller): CRD `validator.operatorKeyring`, mount wiring, validate-keyring task, security context, bootstrap guard generalization
- **#162** (seictl): keyring backend envs, smoke test, generate_gentx refactor

Phase 2 (depends on #219 + #162, ~1-2 weeks):
- **#163** (seictl): sign-tx task family, shared sign-and-broadcast helper, OpenAPI update, client SDK helpers, pre-authn warning notices

Phase 3 (depends on #163, ~1 week):
- **#164** (seictl): seictl gov CLI subcommands, sidecar discovery, fan-out, dry-run

**Genesis-network validation gate** between Phase 3 and Phase 4: deploy a fresh genesis network using the existing `SeiNodeDeployment` genesis-ceremony machinery, exercise the full submit-proposal → vote → upgrade flow end-to-end. Confirm idempotency on simulated sidecar crashes mid-broadcast. This is the gate before opening the surface to real mainnet validators.

Phase 4 (final hardening, ~1 week):
- **#165** (seictl): TokenReview-based authn middleware, audit logging, RBAC additions in the controller for `tokenreviews:create`, seictl CLI token minting, removal of pre-authn warning notices

---

## Alternatives considered

### Shell-out to seid binary in shared PVC

Considered: copy the seid binary into the data PVC; sidecar `exec.Command`s `seid tx gov vote ...` against it.

**Rejected** because:
- Contradicts stated "no shells in pods" direction
- Stdout-parsing-as-IPC is fragile across seid versions
- Version drift between sidecar's expected seid and the actually-mounted binary
- Violates distroless container hygiene
- Trust boundary collapse: seid binary supply-chain compromise → governance tx forgery

Library-based signing using `sei-cosmos/client/tx` (already in seictl's go.mod) preserves all four commitments. Complexity differential is small — ~200-300 lines per Msg family, copy-shape from `sei-cosmos/x/gov/client/cli/tx.go`.

### Generic sign-and-broadcast endpoint with raw Msg bytes

Considered: one task type `sign-and-broadcast` with params `{ chainId, keyName, msgAny }`.

**Rejected** because:
- No typed validation at task submission — invalid Msg shapes only fail at sign time
- Harder to audit ("operator submitted MsgVote on proposal 5" vs "operator submitted opaque tx")
- Harder to apply per-task-type authz when Phase 4+ extends the allowlist model
- No type-safe client SDK helpers

Per-Msg typed handlers cost more lines (each handler ~50 lines vs one generic 100-line handler) but gain operational legibility.

### Block-mode broadcast

Considered: `BroadcastTxBlock` instead of `BroadcastTxSync` + polling.

**Rejected** because:
- Sei's broadcast layer comment at `sei-cosmos/client/broadcast.go:116` explicitly warns: "request may timeout but the tx may still be included" — indeterminate failure state
- Sync mode + explicit polling provides crash-safe semantics and clean retry boundaries
- The polling cost is negligible (one ABCI query at block-time intervals)

### Cached account sequence across retries

Considered: cache `(accountNumber, sequence)` in the engine store; reuse on retry.

**Rejected** because:
- Stale sequence is a footgun — chain may have advanced for unrelated reasons (other operator txs, MsgEditValidator from elsewhere)
- Cost of one extra ABCI query is microseconds; cost of a stuck rebroadcast loop is operator pages
- Always-refresh is the boring-and-correct default

### mTLS for sidecar authn (Phase 4)

Considered: client-cert-based mTLS instead of TokenReview.

**Rejected** for v1 because:
- TokenReview is K8s-native and operator-tooling-native (`kubectl create token` is the operator UX)
- mTLS imposes cert rotation overhead and PKI surface that K8s already provides via SA tokens
- TokenReview is additively replaceable if mTLS becomes desired later

### Per-SeiNode SAs

Considered: controller mints a dedicated SA per SeiNode for blast-radius isolation.

**Deferred** to a future workstream because:
- Single shared `p.ServiceAccount` is sufficient for the coarse allowlist authz of Phase 4
- Per-SeiNode SAs need their own RBAC machinery in the controller, more cluster-level objects, namespacing decisions
- Not needed until multi-tenant clusters or per-validator authz becomes a requirement

### Single Secret for keyring + passphrase

Considered: one Secret with `keyring-file/*.info`, `keyring-file/*.address`, and `passphrase` as siblings; project as volume + extract one key as env.

**Rejected** because:
- Kubernetes Secret volume mounts project ALL data keys as files at the mount path. With one Secret, the passphrase would be projected as a file (`/sei/keyring-file/passphrase`) inside the keyring directory.
- `envFrom: secretRef` projects ALL keys as env vars — the keyring binary blobs would appear in `os.Environ()`.
- Items projection requires enumerating keys at admission time, which the controller cannot do statically.

Two separate Secrets is operationally a small cost; architecturally it preserves the directory-vs-env projection contract cleanly.

---

## Open questions

- **TokenReview cache TTL.** 30s is the recommended default. Worth load-testing in Phase 4 against expected operator-tool call rates.
- **Test backend allowlist.** Should `SEI_KEYRING_BACKEND=test` be allowed in production? Recommendation: refuse if `SEI_CHAIN_ID` matches a known mainnet pattern (e.g., `pacific-1`, `arctic-1`). Implementation detail for #162 reviewers.
- **`UpgradeInfo` semantics for binary handoff.** Sei-specific: does the chain consume `Plan.Info` to download new binaries (e.g., via cosmovisor)? Confirm and document the expected format (URL? structured JSON?) in `docs/gov.md`.
- **Fan-out concurrency model.** The CLI's `--threads` flag is a hard cap; should there be a per-validator timeout to prevent one stuck sidecar from stalling the whole fan-out?
- **Phase 4 sidecar SA migration.** Today `p.ServiceAccount` is shared; Phase 4 adds `tokenreviews:create` to it. Migration to a dedicated `sei-sidecar` SA is a follow-up — tracked separately or rolled into Phase 4? Default recommendation: separate follow-up, after Phase 4 has been operationally validated.

---

## References

### Coral session
This design synthesizes a coral round (2026-05-11) with kubernetes-specialist, blockchain-developer, and platform-engineer specialists.

### GitHub issues
- sei-protocol/sei-k8s-controller#219 — CRD `validator.operatorKeyring`
- sei-protocol/seictl#162 — Sidecar production keyring backend
- sei-protocol/seictl#163 — Sidecar sign-tx task family
- sei-protocol/seictl#164 — seictl gov CLI subcommands
- sei-protocol/seictl#165 — Sidecar authn middleware

### Code references
- `sei-protocol/seictl` — sidecar repo
  - `sidecar/tasks/generate_gentx.go` — in-process SDK signing precedent
  - `sidecar/rpc/client.go` — existing RPC client (GET-shaped)
  - `sidecar/server/server.go` — HTTP routing and middleware install point
  - `sidecar/engine/engine.go:91-138` — UUID dedupe and run-counter
  - `serve.go:42-67` — env-loading pattern
- `sei-protocol/sei-k8s-controller` — controller repo
  - `api/v1alpha1/validator_types.go` — CRD shape to extend (existing `SigningKeySource`, `NodeKeySource`)
  - `internal/noderesource/noderesource.go:242-569` — pod-spec construction
  - `internal/task/validate_signing_key.go` — validation task template
  - `internal/task/bootstrap_resources.go:333-348` — bootstrap-pod isolation guard
  - `internal/planner/planner.go:318-352, 496-509, 540-553` — planner integration points
  - `internal/controller/node/controller.go:53-63` — RBAC marker location
- `sei-protocol/sei-chain` (via go.mod) — Cosmos SDK fork
  - `sei-cosmos/x/gov/types/msgs.go` — `MsgVote`, `MsgSubmitProposal`, `MsgDeposit` constructors
  - `sei-cosmos/x/gov/client/cli/tx.go` — canonical CLI implementation; direct copy-shape target
  - `sei-cosmos/x/upgrade/types/proposal.go:14` — `SoftwareUpgradeProposal`
  - `sei-cosmos/x/upgrade/types/upgrade.pb.go:32-50` — `Plan` struct
  - `sei-cosmos/crypto/keyring/keyring.go:33,42` — file-keyring layout
  - `sei-cosmos/client/tx/factory.go:102-197` — `tx.Factory` builder methods
  - `sei-cosmos/x/auth/types/account_retriever.go:73` — `GetAccountNumberSequence`
  - `sei-cosmos/x/auth/tx/mode_handler.go:11-14` — DefaultSignModes
  - `sei-cosmos/client/broadcast.go:51-67,116` — broadcast modes
  - `sei-cosmos/types/result.go:62-79` — `TxResponse` schema
- `sei-protocol/slanders/seienv` — predecessor tool being replaced
  - `cmd/vote/vote.go:15` — `seid tx gov vote` template; note `--fees 20sei` latent bug
  - `cmd/propose/scripts/propose.sh` — `seid tx gov submit-proposal` template

### Sei-specific config
- `cmd/seid/cmd/root.go:409` — `min-gas-prices = 0.01usei` default
- `1 SEI = 1,000,000 usei` denomination
- Default sign mode: `SIGN_MODE_DIRECT`
- Gov module shape: v1beta1 (not v1 — Sei has not migrated)

---

## Changelog

- 2026-05-11 — Initial draft (coral synthesis)
