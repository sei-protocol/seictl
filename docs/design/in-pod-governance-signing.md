# In-Pod Governance Transaction Signing for Sei Validators

**Status:** Draft (design)
**Scope:** Replace SSH+CLI governance signing (`slanders/seienv`) with in-pod sidecar-based signing for K8s-hosted Sei validators
**Authors:** Brandon Chatham (platform engineer); coral round dispatched kubernetes-specialist, blockchain-developer, platform-engineer
**Last updated:** 2026-05-12
**Revision:** rev2 — kube-rbac-proxy adoption (supersedes rev1's in-process TokenReview middleware design)
**Issues:** sei-protocol/sei-k8s-controller#219 (CRD), sei-protocol/seictl#162 (keyring backend), #163 (sign-tx tasks), #164 (CLI), #165 (authn)
**Workstream:** governance flow migration — replace `seienv` (EC2 + SSH + `seid tx` shell-out) with `seictl` + sidecar

---

## Summary

`slanders/seienv` performs validator governance operations (`MsgVote`, `MsgSubmitProposal`, `MsgDeposit`) by SSH-ing into EC2 validator hosts and shell-ing out to `seid tx gov ...` against the host's local `~/.seid/keyring-file/`. The pattern doesn't translate to Kubernetes — pods don't have SSH, the seid container is being made distroless, and "no shells in pods" is a stated platform direction.

This design replaces that pattern with **library-based signing inside the seictl sidecar container**, using the Cosmos SDK packages already in seictl's `go.mod` (`sei-cosmos/client/tx`, `sei-cosmos/crypto/keyring`). The operator-account keyring lives in a Kubernetes Secret declared on the `SeiNode` CRD, mounted **only** on the sidecar container (never on the seid main container, never on bootstrap pods). The sidecar exposes typed task types (`gov-vote`, `gov-submit-proposal`, `gov-deposit`) over its existing HTTP task API. A new `seictl gov ...` subcommand surfaces this to human operators, with K8s-native authn (kube-rbac-proxy sidecar) gating mutating endpoints in the final phase.

**Precedent is already in tree:** `sidecar/tasks/generate_gentx.go` already opens a `keyring.New(...)`, builds a Cosmos SDK tx, signs in-process, and persists the result. The genesis path uses `BackendTest`; the governance path uses `BackendFile`. Mechanics are identical; only the backend and the broadcast step differ.

The work is decomposed into 5 issues. Phase 1 (#219 + #162) is independent and runs in parallel. Phase 2 (#163) depends on both. Phase 3 (#164) depends on #163. Phase 4 (#165, authn) is a deliberate final hardening pass — the team explicitly opted to ship the core flow first, mirroring today's SSH-key-equivalent trust posture, and close the authn gap once the surface is proven against a genesis network.

## Goals

- **In-pod signing.** Operator-account keyring material never leaves the validator pod's sidecar container. No keys on laptops, no keys on bastion hosts, no keys on EC2 instances.
- **Library-based, not shell-out.** Use the Cosmos SDK packages directly. Preserve the "no shells in pods" architectural commitment. Avoid CLI-stdout-as-IPC fragility, version drift, and trust-boundary collapse.
- **Typed task surface.** Three explicit task types (`gov-vote`, `gov-submit-proposal`, `gov-deposit`) — not a generic "sign-and-broadcast" with raw Msg bytes. Better validation, better audit, easier authz when authn lands.
- **Idempotent, crash-safe.** Caller-supplied UUID dedupe at the engine, plus tx-hash persistence before broadcast at the handler — a retry after a successful broadcast NEVER produces a duplicate transaction.
- **K8s-native trust model.** Authn + authz via kube-rbac-proxy sidecar (Phase 4) terminating TLS and resolving SubjectAccessReviews against standard ClusterRoles; AWS IAM Identity Center → EKS access entries → K8s RBAC for human operators.
- **Single canonical operator tool.** `seictl gov vote ...` replaces `seienv vote ...`. Fan-out across a `SeiNodeDeployment`'s validators is a flag, not a fleet management tool.
- **Composes with existing genesis flow.** `generate_gentx.go`'s in-process signing pattern is the precedent; the new handlers extend the same shape with `BackendFile` and broadcast.

## Non-goals

- **Non-governance transactions.** Staking (`MsgDelegate`, `MsgUndelegate`), distribution (`MsgWithdrawDelegatorReward`), bank (`MsgSend`), IBC — same pattern, future issues if needed.
- **Remote signers.** TMKMS, Horcrux, Vault, AWS KMS, HSM — `OperatorKeyringSource` is a discriminated union with `Secret` as the only initial variant; siblings are reserved as comments only.
- **EVM-side operator transactions.** Sei's Cosmos-EVM address pairing is out of scope. EVM key material, contract ownership, EVM tx submission — not addressed here.
- **Per-SeiNode authz granularity.** Phase 4 ships standard ClusterRoles binding caller identities to verbs on the `seinodes/tasks` virtual subresource. "This SA can vote but not submit-proposal" is deferred.
- **Hot keyring rotation.** Passphrase change requires pod restart in v1. `POST /v0/admin/reload-keyring` is a v1.1 conversation.
- **Multi-tenant cluster deployment.** Phase 1-3 ship without authn; deployment posture is single-tenant operator-controlled. Multi-tenant deployment requires Phase 4 to land first.
- **CRD-managed operator-keyring Secret lifecycle.** The controller never creates, mutates, or deletes the Secret — operators ship it via SOPS / ESO / kubectl, same model as today's `signingKey` and `nodeKey`.

## Architecture

### Component map

```
┌──────────────────────────────────────────────────────────────────────┐
│ Operator workstation                                                 │
│  ┌────────────┐  AWS SSO login → EKS kubeconfig → bearer token (P4)  │
│  │ seictl gov │ ─────────────────────────┐                           │
│  └────────────┘                          │                           │
└──────────────────────────────────────────│───────────────────────────┘
                                           │ HTTPS POST :8443
                                           │ Authorization: Bearer <SA token>
                                           ▼
┌──────────────────────────────────────────────────────────────────────┐
│ K8s Pod (managed by sei-k8s-controller as StatefulSet)               │
│                                                                      │
│  ┌─────────────────────────────┐                                     │
│  │ kube-rbac-proxy             │  :8443 (0.0.0.0, TLS)               │
│  │  - terminates TLS           │ ───────► TokenReview → APIserver    │
│  │  - TokenReview              │ ───────► SubjectAccessReview        │
│  │  - SubjectAccessReview      │                                     │
│  │  - sets X-Remote-User       │                                     │
│  │  - proxies → 127.0.0.1:7777 │                                     │
│  └──────────────┬──────────────┘                                     │
│                 │ loopback HTTP                                      │
│                 ▼                                                    │
│  ┌──────────────────────────┐    ┌────────────────────────────┐      │
│  │ seictl sidecar           │    │ seid main container        │      │
│  │  (distroless)            │    │  (chain runtime)           │      │
│  │  binds 127.0.0.1:7777    │    │                            │      │
│  │                          │    │ /sei/config/               │      │
│  │  ┌────────────────────┐  │    │   priv_validator_key.json  │      │
│  │  │ keyring (in-mem)   │  │    │   node_key.json            │      │
│  │  │  opened at startup │  │    │ /sei/data/                 │      │
│  │  └────────────────────┘  │    │                            │      │
│  │  /sei/keyring-file/      │    │ RPC :26657                 │      │
│  │   <key>.info             │    │  ◄────── localhost ────────┼──────┘
│  │   <hex>.address          │    │                            │
│  └──────────────────────────┘    └────────────────────────────┘
│        ▲                                                             │
│        │ mount mode 0o400, sidecar only                              │
│  ┌─────┴─────────────────────────────────────────────────────┐       │
│  │ Secret (kubectl/SOPS/ESO) — operator-managed              │       │
│  │   keyring-file/<key>.info                                 │       │
│  │   keyring-file/<hex>.address                              │       │
│  └───────────────────────────────────────────────────────────┘       │
│  ┌───────────────────────────────────────────────────────────┐       │
│  │ Secret (separate) — passphrase only                       │       │
│  │   passphrase: <pw>                                        │       │
│  └───────────────────────────────────────────────────────────┘       │
│  ┌───────────────────────────────────────────────────────────┐       │
│  │ Secret (cert-manager) — TLS for kube-rbac-proxy           │       │
│  │   tls.crt / tls.key                                       │       │
│  └───────────────────────────────────────────────────────────┘       │
└──────────────────────────────────────────────────────────────────────┘
```

| Component | Repo | Issue |
|---|---|---|
| **A.** `SeiNode.validator.operatorKeyring` CRD field, validation task, sidecar-only mount | sei-k8s-controller | #219 |
| **B.** Sidecar keyring backend (envs, smoke test, factory) | seictl | #162 |
| **C.** Sign-tx task family (gov-vote, gov-submit-proposal, gov-deposit) | seictl | #163 |
| **D.** seictl `gov` CLI subcommands | seictl | #164 |
| **E.** Sidecar authn + authz via kube-rbac-proxy (seictl + sei-k8s-controller) | seictl + sei-k8s-controller | #165 |

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
    participant Proxy as kube-rbac-proxy<br/>(0.0.0.0:8443)
    participant SC as seictl sidecar<br/>(127.0.0.1:7777)
    participant SD as seid<br/>(local RPC :26657)
    participant Chain as Chain

    Op->>K8s: GET SeiNode/<name>
    K8s-->>Op: spec + status
    Op->>Op: resolve sidecar URL<br/>(headless Service DNS, https://...:8443)
    Op->>K8s: TokenRequest (Phase 4)
    K8s-->>Op: bearer token
    Op->>Proxy: POST /v0/tasks/gov/vote<br/>{params, taskId:UUID}<br/>Authorization: Bearer ...
    Proxy->>K8s: TokenReview
    K8s-->>Proxy: authenticated identity
    Proxy->>K8s: SubjectAccessReview<br/>{verb:create, resource:seinodes/tasks, subresource:gov.vote}
    K8s-->>Proxy: allowed
    Proxy->>SC: POST /v0/tasks/gov/vote<br/>X-Remote-User: <caller><br/>(loopback HTTP)
    SC->>SC: dedupe by UUID;<br/>dispatch handler;<br/>record caller on task
    SC->>SD: GET /status
    SD-->>SC: node_info.network
    SC->>SC: chain-id guard:<br/>params.chainId == status.network
    SC->>SC: open keyring; resolve key
    SC->>SD: ABCI /auth.Query/Account
    SD-->>SC: accountNumber, sequence
    SC->>SC: build MsgVote;<br/>tx.Factory; Sign
    SC->>SC: compute txHash =<br/>sha256(txBytes);<br/>persist {taskId, txHash, caller}
    SC->>SD: BroadcastTxSync(txBytes)
    SD-->>SC: TxResponse{code, txHash}
    SC->>SD: poll /tx?hash=...
    SD-->>SC: txResult{height, gasUsed}
    SC->>SC: persist final result
    Op->>Proxy: GET /v0/tasks/{id} (poll)
    Proxy->>SC: GET /v0/tasks/{id}
    SC-->>Op: {status:completed, txHash, height, caller}
```

On crash before step 20 (broadcast), the engine restart re-runs the handler with the same UUID. The handler queries `/tx?hash=<persistedHash>`:

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

### Sidecar TLS CRD additions (Phase 4)

Add to `api/v1alpha1/seinode_types.go` alongside the existing sidecar spec:

```go
// SidecarTLSSpec configures the kube-rbac-proxy front-end TLS for the
// sidecar's management API. Activates the proxy container; absent means
// the sidecar runs without the proxy and binds 0.0.0.0:7777 (Phase 1-3
// posture).
type SidecarTLSSpec struct {
    // IssuerRef references a cert-manager Issuer or ClusterIssuer that
    // signs the proxy's serving certificate. The controller emits a
    // cert-manager Certificate resource that requests a leaf cert for
    // the sidecar's headless Service DNS name; cert-manager populates
    // the resulting Secret which the proxy mounts.
    //
    // +kubebuilder:validation:Required
    IssuerRef CertManagerIssuerRef `json:"issuerRef"`
}

// CertManagerIssuerRef mirrors cert-manager's ObjectReference shape so the
// CRD does not depend on the cert-manager API surface at compile time.
type CertManagerIssuerRef struct {
    // +kubebuilder:validation:Required
    Name string `json:"name"`
    // Kind is "Issuer" or "ClusterIssuer". Defaults to "Issuer".
    // +kubebuilder:default="Issuer"
    // +kubebuilder:validation:Enum=Issuer;ClusterIssuer
    Kind string `json:"kind,omitempty"`
    // Group is "cert-manager.io". Defaults so.
    // +kubebuilder:default="cert-manager.io"
    Group string `json:"group,omitempty"`
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

### Planner integration delta (Phase 4)

When `spec.sidecar.tls` is set, the planner emits two additional resources before the StatefulSet apply:

```go
if needsSidecarTLS(node) { prog = append(prog, task.TaskTypeApplySidecarCertificate) }
if needsSidecarTLS(node) { prog = append(prog, task.TaskTypeApplyRBACProxyConfigMap) }
```

`ApplySidecarCertificate` emits a `cert-manager.io/v1` Certificate naming the headless Service DNS pattern (`*.<svc>.<ns>.svc.cluster.local` plus the bare `<svc>.<ns>.svc.cluster.local`). `ApplyRBACProxyConfigMap` emits the proxy's `--config-file` ConfigMap with the `resourceAttributes` mapping (see Component E).

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

### Pod-network exposure (added in Phase 4)

The sidecar's HTTP API is no longer exposed on the pod network directly; Phase 4 binds the sidecar to loopback and fronts it with kube-rbac-proxy. The resulting per-container port table:

| Container | Bind address | Port | Protocol | Listener | Exposed via Service |
|---|---|---|---|---|---|
| seictl sidecar | `127.0.0.1` | 7777 | HTTP (plain) | task API | no |
| kube-rbac-proxy | `0.0.0.0` | 8443 | HTTPS (cert-manager) | TLS terminator → loopback | yes — Service port `api` |
| seid | `0.0.0.0` | 26656 | TCP | P2P | yes (P2P only) |
| seid | `127.0.0.1` | 26657 | TCP | Tendermint RPC | no (intra-pod only) |
| seid | `127.0.0.1` | 1317 | TCP | Cosmos REST | no |
| seid | `127.0.0.1` | 8545 / 8546 | TCP | EVM JSON-RPC / WS | no |
| seid | `127.0.0.1` | 9090 | TCP | gRPC | no |
| seid | `127.0.0.1` | 26660 | TCP | Prometheus metrics | no |

Only `8443` (proxy) and `26656` (P2P) leave the pod by design.

### Service spec delta

The headless Service grows one new port and renames the existing one for clarity:

```go
corev1.ServicePort{Name: "api", Port: 8443, TargetPort: intstr.FromInt(8443)}  // Phase 4: rbac-proxy HTTPS
corev1.ServicePort{Name: "p2p", Port: 26656, TargetPort: intstr.FromInt(26656)}
```

In Phase 1-3 (no `spec.sidecar.tls`), the Service exposes `:7777` plain HTTP as today. The controller switches the port spec based on `needsSidecarTLS(node)`.

### RBAC

**Existing coverage:** `+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch` on `internal/controller/node/controller.go:63` is sufficient for reading both the keyring Secret and the passphrase Secret. **No new permission needed for Phase 1.**

### cert-manager dependency (Phase 4)

Phase 4 requires cert-manager to be installed in the cluster. The controller's pre-flight validates that a `cert-manager.io/v1` `Issuer` (or `ClusterIssuer`) named in `spec.sidecar.tls.issuerRef` exists and is `Ready=True` before emitting the Certificate. The controller does NOT install or manage cert-manager itself.

If cert-manager is not present, `spec.sidecar.tls` admission fails: "cert-manager.io CRDs not found in cluster — install cert-manager (https://cert-manager.io/docs/) or omit spec.sidecar.tls to run with the Phase 1-3 plain-HTTP posture." There is intentionally no self-signed fallback; the bootstrap surface is small enough that operators standardize on cert-manager.

### AutomountServiceAccountToken — clarified

The sidecar pod uses kubelet's default `automountServiceAccountToken: true` so that kube-rbac-proxy can read its own SA token to construct TokenReview / SubjectAccessReview requests against the API server. (The sidecar container itself no longer makes any K8s API calls in Phase 4 — that responsibility moves to the proxy.) Future hardening could drop the projected token from the sidecar's container view via per-container `volumeMounts` exclusions, but that's deferred until a concrete blast-radius case justifies the manifest churn.

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

// TaskClass groups task types for URL routing and authz. The class is the
// second URL segment after /v0/tasks; the type is the third. Authz lives
// at the class level (one ClusterRole verb per class).
type TaskClass string

const (
    TaskClassGov TaskClass = "gov"
)

// TaskRegistration binds a (class, type) pair to its handler factory.
type TaskRegistration struct {
    Class   TaskClass
    Type    TaskType
    Factory HandlerFactory
}

var taskRegistry = []TaskRegistration{
    {TaskClassGov, TaskTypeGovVote, newGovVoteExecution},
    {TaskClassGov, TaskTypeGovSubmitProposal, newGovSubmitProposalExecution},
    {TaskClassGov, TaskTypeGovDeposit, newGovDepositExecution},
    // ... existing registrations migrated to this shape ...
}
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

### API path shape (one-way door)

The sidecar's task API moves from a single `POST /v0/tasks` endpoint with the task type embedded in the body to a path-routed shape with the (class, type) pair in the URL. This is necessary for kube-rbac-proxy to enforce per-class authz against the K8s API server's SubjectAccessReview — the proxy's `resourceAttributes` config selects the verb/resource from URL segments, not from the request body.

| | rev1 (one path) | rev2 (class/type in path) |
|---|---|---|
| Submit | `POST /v0/tasks` (type in body) | `POST /v0/tasks/<class>/<type>` |
| Read | `GET /v0/tasks/<id>` | `GET /v0/tasks/<id>` (unchanged) |
| List | `GET /v0/tasks` | `GET /v0/tasks` (unchanged) |
| Examples | `POST /v0/tasks` `{type:gov-vote, ...}` | `POST /v0/tasks/gov/vote` `{...}` |

Routing sketch (`sidecar/server/server.go`):

```go
mux.HandleFunc("POST /v0/tasks/{class}/{type}", func(w http.ResponseWriter, r *http.Request) {
    class := TaskClass(r.PathValue("class"))
    typ   := TaskType(r.PathValue("type"))
    reg, ok := lookupRegistration(class, typ)
    if !ok {
        writeError(w, http.StatusNotFound, "unknown task class/type")
        return
    }
    // ... decode params, submit to engine ...
})
```

This shape is a **one-way door**: once shipped, third-party operator tooling will encode URL patterns. Reverting to a body-routed shape would break authz at the proxy boundary. A future need for `<verb>` (e.g. `gov-vote/dry-run`) is handled additively via query param `?action=dry-run`, not by extending the path.

### Caller attribution on the task record

When kube-rbac-proxy authn/authz passes, it sets `X-Remote-User: <username>` on the proxied request. The sidecar reads this header on the loopback ingress and persists the caller alongside the task record:

```go
caller := r.Header.Get("X-Remote-User")
// trust derives from the bind-127.0.0.1 invariant — see Component E
task.CreatedBy = caller
```

Caller is exposed on `GET /v0/tasks/{id}` and surfaced by the CLI's rendered output. Audit logs include `caller` on every mutating request.

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
  "broadcastedAt": "2026-05-12T...",
  "includedAt":    "2026-05-12T...",
  "createdBy":     "<X-Remote-User from rbac-proxy>",
  // task-type-specific extension:
  "proposalId":    123                         // for gov-vote and gov-deposit
}
```

Minimum required: `{txHash, code, height, rawLog}`. The rest are operator-debug-grade and cheap to include.

### OpenAPI schema update

Update `sidecar/api/openapi.yaml`:
- Replace the single `POST /v0/tasks` endpoint with the path-routed shape: `POST /v0/tasks/gov/vote`, `POST /v0/tasks/gov/submit-proposal`, `POST /v0/tasks/gov/deposit`. Per-type param schema components.
- Add the result schema under the task result component, including `createdBy`.
- Add `securitySchemes: bearerAuth` and apply to all mutating operations; document that authn is enforced by kube-rbac-proxy at the cluster boundary, not in-process.
- Add a top-level note: "Sign-tx tasks ship unauthenticated in Phase 1-3; see issue #165 for the kube-rbac-proxy enablement plan."
- Drop any rev1 references to in-process TokenReview / `SEI_AUTHZ_*` env vars.

### Client SDK helpers

Add to `sidecar/client/tasks.go`:

```go
func (c *SidecarClient) SubmitGovVoteTask(ctx context.Context, taskID string, p GovVoteParams) (*Task, error)
func (c *SidecarClient) SubmitGovSubmitProposalTask(ctx context.Context, taskID string, p GovSubmitProposalParams) (*Task, error)
func (c *SidecarClient) SubmitGovDepositTask(ctx context.Context, taskID string, p GovDepositParams) (*Task, error)
```

Each thin wrapper around the right `POST /v0/tasks/<class>/<type>` endpoint.

### Pre-authn warning notice

Until Phase 4 authn lands, each new handler file carries a top-of-file comment:

```go
// SECURITY POSTURE NOTE — sei-protocol/seictl#163 / #165
//
// This handler accepts sign-and-broadcast requests over the sidecar's HTTP
// API, which is unauthenticated in Phase 1-3 of the governance-flow workstream.
// The sidecar binds 0.0.0.0:7777 in Phase 1-3; any caller with network reach
// to that port can submit governance txs as the validator's operator account.
// This is comparable to the seienv+SSH status-quo trust scope (anyone with
// the SSH key has equivalent power) but the K8s network blast radius is wider.
//
// Phase 4 (#165) fronts the sidecar with a kube-rbac-proxy sidecar container
// that terminates TLS on 0.0.0.0:8443, runs TokenReview + SubjectAccessReview
// against the cluster API server, and proxies to the sidecar bound on
// 127.0.0.1:7777. The sidecar then trusts X-Remote-User on the loopback ingress.
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

The CLI resolves the sidecar URL from the target SeiNode's headless Service. Phase 4 returns `https://...:8443`; Phase 1-3 returns `http://...:7777`. The toggle is whether the SeiNode has `spec.sidecar.tls` set:

```go
func resolveSidecarURL(ctx context.Context, dyn dynamic.Interface, name, ns string) (string, error) {
    obj, err := dyn.Resource(seinodeGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
    if err != nil { return "", err }
    tlsConfigured, _, _ := unstructured.NestedMap(obj.Object, "spec", "sidecar", "tls")
    host := fmt.Sprintf("%s-0.%s.%s.svc.cluster.local", name, name, ns)
    if tlsConfigured != nil {
        return fmt.Sprintf("https://%s:8443", host), nil
    }
    return fmt.Sprintf("http://%s:7777", host), nil
}
```

Operators running seictl from outside the cluster override via `--sidecar-url`, or use `kubectl port-forward` separately. The default in-cluster pattern is the recommended path.

### Authentication (Phase 4)

Phase 4 uses the operator's kubeconfig as the source of truth for credentials. seictl invokes `client-go`'s standard rest-config loader, which automatically resolves bearer tokens from exec credential plugins (AWS `aws eks get-token`, GKE `gke-gcloud-auth-plugin`, OIDC `kubectl-oidc_login`, etc.). The resolved token is presented on the outgoing request:

```go
restCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
    loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
if err != nil { return err }
// restCfg.BearerToken (or .ExecProvider) supplies the credential for HTTPS calls.
tr, err := rest.TransportFor(restCfg)
if err != nil { return err }
httpClient := &http.Client{Transport: tr}
```

The TLS root CA for verifying the proxy's serving certificate comes from the cert-manager `Issuer` chain. The CLI prefers, in order:
1. `--sidecar-ca-file <path>` if provided
2. The `ConfigMap`/`Secret` named in the SeiNode's `spec.sidecar.tls.issuerRef` that exposes the trust root
3. The system CA bundle (sufficient when the Issuer chains to a public root)

If none verifies, the CLI errors with a clear remediation hint; it does NOT fall back to `InsecureSkipVerify`. There is no in-tree mTLS path for v1.

### AWS SSO operator flow

The expected human-operator flow on AWS-managed EKS:

```
$ aws sso login --profile validator-operator
Successfully logged into Start URL: https://<org>.awsapps.com/start

$ aws eks update-kubeconfig --name <cluster> --profile validator-operator
Added new context arn:aws:eks:...:cluster/<cluster> to ~/.kube/config

$ seictl gov vote 5 yes --validator val-0 --namespace sei-validators
# client-go invokes `aws eks get-token` via the kubeconfig exec credential
# plugin; the resulting bearer token is presented to kube-rbac-proxy; the
# proxy verifies it via TokenReview against EKS, then runs SubjectAccessReview;
# the request is forwarded to the sidecar with X-Remote-User set.
```

The mapping from AWS IAM principal to K8s identity is handled by EKS access entries (Path A); see Component E.

### Output

Default `--wait=true`: poll `GET /v0/tasks/{id}` until terminal, render result:

```
$ seictl gov vote 5 yes --validator my-validator
Submitted task abc-123 to https://my-validator-0...:8443
Waiting for inclusion ...
✓ tx 0xB7E2... included at height 1234567 (code=0, gasUsed=142318)
  as: arn:aws:sts::123456789012:assumed-role/AWSReservedSSO_ValidatorOperator/brandon
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

### CLI output addition

Phase 4 adds an `as: <identity>` line to single-validator rendered output (above) and a separate `--show-caller` flag for fan-out mode that adds a `CALLER` column to the table. The identity is whatever the kube-rbac-proxy resolved and persisted as `createdBy` on the task — typically the full `arn:aws:sts::.../AWSReservedSSO_*` SSO assumed-role ARN on EKS.

### Idempotent retry

`--task-id <uuid>` lets an operator retry safely: same UUID → engine dedupe → same outcome. If absent, the CLI generates and prints the UUID so a retry is trivial.

### Documentation

New `docs/gov.md` mirroring the structure of `slanders/seienv/cheatsheet.md`'s governance section. Cross-reference from `docs/design/in-pod-governance-signing.md` (this file) and the new design's own README pointer.

---

## Component E — Authn + authz via kube-rbac-proxy (seictl + sei-k8s-controller)

### Why kube-rbac-proxy

The rev1 design landed authn + a static-allowlist authz layer inside the seictl sidecar binary (in-process TokenReview middleware + `SEI_AUTHZ_ALLOWED_CALLERS` env var). Reconsideration during the Phase 4 design pass surfaced three load-bearing problems:

1. **Authz drift.** A static comma-separated env var is the wrong shape for a K8s-native authz layer — there's no `kubectl auth can-i`, no RoleBinding audit trail, no standard mechanism for granting operator humans the same privileges as the controller SA.
2. **Cross-repo coupling.** Every authz change requires a sidecar rebuild + redeploy. Cluster operators expect to grant/revoke access via `kubectl apply` against ClusterRoleBinding manifests, not by editing controller env vars.
3. **No AWS integration story.** Human-operator access (AWS SSO → EKS) has no clean path in the env-allowlist model. The `system:serviceaccount:...` shape isn't what `aws eks get-token` produces.

[kube-rbac-proxy](https://github.com/brancz/kube-rbac-proxy) is the standard K8s solution for this problem. It runs as a sidecar container, terminates TLS, validates incoming bearer tokens via TokenReview, runs SubjectAccessReview against the cluster's RBAC, and proxies authenticated/authorized requests to a backend (here, the seictl sidecar on loopback). Adoption is widespread (kube-state-metrics, kube-prometheus-stack, OperatorHub) and the operational model maps directly to K8s RBAC.

### Architecture

```
                                ┌──────────────────────────────────────┐
                                │ K8s API server                       │
                                │  /apis/authentication.k8s.io         │
                                │  /apis/authorization.k8s.io          │
                                └──────────────────────────────────────┘
                                              ▲           ▲
                                              │ TokenReview / SAR
                                              │
┌──────────────────────────────────────────────┼──────────┴───────────────┐
│ SeiNode pod                                  │                          │
│                                              │                          │
│  ┌──────────────────────────┐                │                          │
│  │ kube-rbac-proxy          │ ◄────────  HTTPS :8443 (TLS, cert-manager)│
│  │                          │ ───────────────┘                          │
│  │  /v0/* → SAR config      │                                           │
│  │   verb=create/get/list   │                                           │
│  │   resource=seinodes/tasks│                                           │
│  │   subresource=gov.vote   │                                           │
│  │                          │                                           │
│  │  passes X-Remote-User    │                                           │
│  │  passes X-Remote-Group   │ ───► HTTP loopback                        │
│  └──────────────────────────┘                                           │
│             │                                                           │
│             ▼ 127.0.0.1:7777                                            │
│  ┌──────────────────────────┐                                           │
│  │ seictl sidecar           │                                           │
│  │  binds 127.0.0.1 only    │                                           │
│  │  trusts X-Remote-User    │                                           │
│  │  on loopback ingress     │                                           │
│  └──────────────────────────┘                                           │
└─────────────────────────────────────────────────────────────────────────┘
```

### Auth flow

1. CLI presents `Authorization: Bearer <token>` to `https://<pod>:8443/v0/tasks/gov/vote`.
2. kube-rbac-proxy strips and validates via `POST /apis/authentication.k8s.io/v1/tokenreviews`.
3. On authenticated success, the proxy maps the URL path to `(group, resource, subresource, verb)` via its `resourceAttributes` config, runs `POST /apis/authorization.k8s.io/v1/subjectaccessreviews`, and proceeds only if `status.allowed=true`.
4. The proxy adds `X-Remote-User`, `X-Remote-Group` (multi-valued), and `X-Remote-Extra-*` headers to the proxied request.
5. The proxy forwards to `http://127.0.0.1:7777` over loopback. The sidecar reads `X-Remote-User`, records it on the task, and serves the request.
6. Read-only endpoints (`GET /v0/tasks*`, `/v0/healthz`, `/v0/livez`) are configured `--allow-paths` / `--ignore-paths` in the proxy so they bypass SAR.

### URL convention

`POST /v0/tasks/<class>/<type>` (defined in Component C). Class drives the SAR `subresource`:

| Path | SAR verb | SAR resource | SAR subresource |
|---|---|---|---|
| `POST /v0/tasks/gov/vote` | `create` | `seinodes/tasks` | `gov.vote` |
| `POST /v0/tasks/gov/submit-proposal` | `create` | `seinodes/tasks` | `gov.submitproposal` |
| `POST /v0/tasks/gov/deposit` | `create` | `seinodes/tasks` | `gov.deposit` |
| `GET /v0/tasks` | (bypass) | — | — |
| `GET /v0/tasks/{id}` | (bypass) | — | — |

The `seinodes/tasks` "resource" is a virtual subresource — there's no real K8s resource by that name. SAR doesn't require one to exist; it's a label that ClusterRoles can grant. Using `seinodes/tasks` (rather than something CRD-specific) keeps the authz surface stable across SeiNode kind extensions.

### resourceAttributes config

The proxy ConfigMap (`<seinode>-rbac-proxy-config`) the controller emits:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-validator-rbac-proxy-config
  namespace: sei-validators
data:
  config.yaml: |
    authorization:
      rewrites:
        byHTTPPath:
          path: "/v0/tasks/{class}/{type}"
      resourceAttributes:
        apiGroup: "sei.network"
        resource: "seinodes/tasks"
        subresource: "{class}.{type}"
        namespace: "sei-validators"
        name: "my-validator"
        verb: "create"
      allowedPaths:
        - "/v0/healthz"
        - "/v0/livez"
        - "/v0/metrics"
        - "/v0/tasks"           # GET list bypass
        - "/v0/tasks/*"         # GET by-id bypass; mutating POST handled by resourceAttributes above
```

(The exact `allowedPaths` vs. `ignorePaths` mechanics depend on kube-rbac-proxy version — pin a recent release and verify the read-bypass path during Phase 4 implementation.)

### TLS cert source

The proxy serves TLS on `:8443`. The certificate comes from a cert-manager `Issuer` or `ClusterIssuer` referenced via `spec.sidecar.tls.issuerRef` on the SeiNode (see Component A). The controller emits a `Certificate` resource:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: my-validator-sidecar-tls
  namespace: sei-validators
spec:
  secretName: my-validator-sidecar-tls
  duration: 2160h          # 90 days
  renewBefore: 360h        # 15 days
  issuerRef:
    name: <from spec.sidecar.tls.issuerRef.name>
    kind: <Issuer|ClusterIssuer>
    group: cert-manager.io
  commonName: my-validator.sei-validators.svc.cluster.local
  dnsNames:
    - my-validator.sei-validators.svc.cluster.local
    - my-validator-0.my-validator.sei-validators.svc.cluster.local
```

The `Issuer` itself is operator-owned — cluster operators choose self-signed-CA, internal CA, ACME, or a managed CA backend as appropriate. The controller is PCA-agnostic; the only assumption is `cert-manager.io/v1` API availability.

### Pod-spec additions

The controller adds two items when `spec.sidecar.tls` is set:

```go
// Container
proxyContainer := corev1.Container{
    Name:  "kube-rbac-proxy",
    Image: "quay.io/brancz/kube-rbac-proxy:v0.19.0",
    Args: []string{
        "--secure-listen-address=0.0.0.0:8443",
        "--upstream=http://127.0.0.1:7777/",
        "--config-file=/etc/kube-rbac-proxy/config.yaml",
        "--tls-cert-file=/etc/tls/tls.crt",
        "--tls-private-key-file=/etc/tls/tls.key",
        "--logtostderr=true",
        "--v=2",
    },
    Ports: []corev1.ContainerPort{{Name: "api", ContainerPort: 8443, Protocol: corev1.ProtocolTCP}},
    VolumeMounts: []corev1.VolumeMount{
        {Name: "rbac-proxy-config", MountPath: "/etc/kube-rbac-proxy", ReadOnly: true},
        {Name: "sidecar-tls",       MountPath: "/etc/tls",             ReadOnly: true},
    },
    SecurityContext: &corev1.SecurityContext{
        RunAsNonRoot:             ptr.To(true),
        RunAsUser:                ptr.To[int64](65532),
        AllowPrivilegeEscalation: ptr.To(false),
        ReadOnlyRootFilesystem:   ptr.To(true),
        Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
        SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
    },
}

// Volumes
volumes := []corev1.Volume{
    {Name: "rbac-proxy-config", VolumeSource: corev1.VolumeSource{
        ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{
            Name: node.Name + "-rbac-proxy-config",
        }},
    }},
    {Name: "sidecar-tls", VolumeSource: corev1.VolumeSource{
        Secret: &corev1.SecretVolumeSource{SecretName: node.Name + "-sidecar-tls", DefaultMode: ptr.To[int32](0o400)},
    }},
}
```

### Sidecar bind change

`sidecar/server/server.go` binds the listener based on env:

```go
addr := "0.0.0.0:7777"
if os.Getenv("SEI_SIDECAR_LOOPBACK_ONLY") == "true" {
    addr = "127.0.0.1:7777"
}
listener, err := net.Listen("tcp", addr)
```

The controller injects `SEI_SIDECAR_LOOPBACK_ONLY=true` on the sidecar container when `spec.sidecar.tls` is set. Phase 1-3 (no TLS spec) leaves it unset and the sidecar binds `0.0.0.0:7777` as today.

### Header reading

```go
// CallerFromRequest extracts the kube-rbac-proxy-injected identity headers
// from a request that arrived on the loopback ingress.
//
// SECURITY POSTURE — DO NOT REMOVE
// Trusting X-Remote-* is sound ONLY because the sidecar listener binds
// 127.0.0.1 in Phase 4 (SEI_SIDECAR_LOOPBACK_ONLY=true). The Linux kernel
// guarantees that loopback traffic never leaves the network namespace;
// the only way to reach 127.0.0.1:7777 is to be inside the pod's net ns,
// which means being kube-rbac-proxy (or a malicious sibling container,
// which is a broader compromise that all loopback-trust patterns share).
// If the bind ever regresses to 0.0.0.0 without removing this trust, an
// off-pod attacker can spoof X-Remote-User. Asserted at startup; see
// the bind check in serve.go and the e2e test in
// sidecar/server/auth_test.go.
func CallerFromRequest(r *http.Request) string {
    return r.Header.Get("X-Remote-User")
}
```

### RBAC manifests

Two standard ClusterRoles ship with the controller chart. Operators bind them to identities via ClusterRoleBinding manifests.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: sei-validator-governance-operator
rules:
  - apiGroups: ["sei.network"]
    resources: ["seinodes/tasks"]
    resourceNames: []   # unrestricted in v1; per-validator narrowing is operator-driven
    verbs: ["create", "get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: sei-validator-lifecycle-controller
rules:
  - apiGroups: ["sei.network"]
    resources: ["seinodes/tasks"]
    verbs: ["create", "get", "list"]
```

Example bindings:

```yaml
# Bind the sei-k8s-controller's own SA so it can submit non-gov lifecycle tasks.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: sei-k8s-controller-tasks
subjects:
  - kind: ServiceAccount
    name: sei-k8s-controller-manager
    namespace: sei-k8s-controller-system
roleRef:
  kind: ClusterRole
  name: sei-validator-lifecycle-controller
  apiGroup: rbac.authorization.k8s.io
---
# Bind an SSO-derived group for human operators.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: governance-operators-binding
subjects:
  - kind: Group
    name: validator-governance-operators
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: sei-validator-governance-operator
  apiGroup: rbac.authorization.k8s.io
```

### AWS IAM Identity Center → EKS integration (Path A: EKS access entries)

For AWS-managed EKS clusters, the recommended (and only documented) path maps IAM Identity Center permission sets to K8s RBAC via EKS access entries.

Setup:

1. **Permission set in IAM Identity Center.** Create a `ValidatorOperator` permission set with an inline policy granting `eks:DescribeCluster` on the target cluster ARN. No other AWS-side permissions required.
2. **EKS access entry.** For each cluster, create an access entry mapping the SSO-provisioned IAM role to a K8s username + groups:
   ```
   aws eks create-access-entry \
     --cluster-name <cluster> \
     --principal-arn arn:aws:iam::<acct>:role/aws-reserved/sso.amazonaws.com/AWSReservedSSO_ValidatorOperator_<hash> \
     --kubernetes-groups validator-governance-operators \
     --type STANDARD
   ```
3. **ClusterRoleBinding** (above) binds the `validator-governance-operators` group to the `sei-validator-governance-operator` ClusterRole.

The flow at request time:

```
aws sso login --profile validator-operator
  → SSO issues an AssumeRoleWithSAML credential
aws eks get-token --cluster-name <cluster>
  → returns a signed EKS-flavored token whose subject is the SSO role
client-go presents the token to kube-rbac-proxy
  → kube-rbac-proxy → API server TokenReview
    → API server resolves the access entry → username + groups
  → kube-rbac-proxy → API server SubjectAccessReview
    → groups match validator-governance-operators → ClusterRole allows
  → proxy forwards to sidecar with X-Remote-User + X-Remote-Group set
```

The username surfaced as `X-Remote-User` is the full assumed-role ARN; this is what shows up in the CLI's `as: <identity>` line and in the sidecar's audit log.

### Phased rollout

| Phase | sidecar bind | proxy installed? | TLS | Authn | Authz | Caller attribution |
|---|---|---|---|---|---|---|
| 1-3 (today) | `0.0.0.0:7777` plain HTTP | no | none | none | none | none |
| 4 (#165) | `127.0.0.1:7777` plain HTTP | yes (sidecar container) | cert-manager `:8443` | TokenReview at proxy | SubjectAccessReview at proxy | `X-Remote-User` recorded on task |

The Phase 4 transition is per-SeiNode and opt-in: setting `spec.sidecar.tls` flips the bind, adds the proxy container, and emits the Certificate + ConfigMap. SeiNodes without `spec.sidecar.tls` continue on the Phase 1-3 posture until operators migrate them. There is no hidden defaulting; the operator chooses by adding the field.

### Removal of the rev1 design

When Phase 4 lands, the following rev1 surfaces are deleted (not deprecated):

- `sidecar/server/auth.go` middleware (in-process TokenReview + allowlist)
- `SEI_AUTHZ_ENABLED` and `SEI_AUTHZ_ALLOWED_CALLERS` env vars
- The TokenReview cache logic
- Pre-authn warning notices in Component C handler files and the README

The controller's `+kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create` marker also goes away — the controller's own SA no longer needs `tokenreviews:create`. That permission moves to the proxy's own SA.

### RBAC for the proxy's own SA

The kube-rbac-proxy container runs under a per-SeiNode (or chart-shared) SA with two cluster-level permissions:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: sei-rbac-proxy
rules:
  - apiGroups: ["authentication.k8s.io"]
    resources: ["tokenreviews"]
    verbs: ["create"]
  - apiGroups: ["authorization.k8s.io"]
    resources: ["subjectaccessreviews"]
    verbs: ["create"]
```

A `ClusterRoleBinding` from this role to the proxy's SA ships with the controller chart. The seictl sidecar's SA no longer needs either of these.

---

## Security posture

### Trust boundary summary

| Component | Has signing key? | Has node key? | Has operator keyring? | Has K8s SA token? | Can authn/authz incoming requests? |
|---|---|---|---|---|---|
| Operator laptop | No (Phase 4: bearer token from kubeconfig exec credential) | No | No | No (uses kubeconfig identity) | n/a (client) |
| seid main container | **Yes** (consensus) | **Yes** (P2P) | No | Yes (kubelet default) | n/a (no exposed mgmt API) |
| seictl sidecar | No | No | **Yes** (governance) | Yes (kubelet default; unused in Phase 4) | No (trusts `X-Remote-User` from loopback) |
| kube-rbac-proxy | No | No | No | Yes (used for TokenReview / SAR) | **Yes** (authoritative authn/authz front door) |
| Bootstrap pod | No | No | No | Yes (kubelet default) | n/a (no exposed mgmt API) |

Key separation rationale:
- A compromised seid binary signs blocks (consensus key) but cannot vote on proposals (no operator keyring).
- A compromised sidecar can vote/propose but cannot sign blocks or impersonate the P2P peer. It also cannot authenticate callers — that authority lives in a different container.
- A compromised bootstrap pod runs `seid start --halt-height` with no on-chain identity; cannot sign anything.
- A compromised kube-rbac-proxy can fabricate `X-Remote-User`, but it cannot directly sign — it would have to convince the sidecar to invoke a sign handler, which is still subject to the chain-confusion guard, idempotency dedupe, and audit logging. The blast radius is "the proxy can submit any task as any identity," not "the proxy can mint blocks."

This is materially stronger than seienv's "operator account key on every EC2 host that runs seid" model.

### Loopback-binding trust model

The Phase 4 design rests on a single load-bearing invariant: **the seictl sidecar's management listener binds `127.0.0.1` and never `0.0.0.0` whenever it is configured to trust `X-Remote-User` from inbound requests**. If that invariant breaks, an off-pod attacker with cluster network reach can spoof any caller identity by setting the header themselves.

Threat: loopback is shared at the pod-network-namespace level. Any container inside the same pod can `connect(127.0.0.1:7777)` and present a forged `X-Remote-User`. This is fine in the current pod composition (sidecar + seid + proxy + a possible init container), all of which are first-party images. It would NOT be fine if an arbitrary user-supplied sidecar were ever co-tenant in the pod — none is, and `SeiNode.spec` deliberately gives no surface to inject one.

Mitigations:

1. **Bind check at startup.** The sidecar reads its own listen address and refuses to start if `SEI_SIDECAR_LOOPBACK_ONLY=true` resolved to a non-loopback bind (e.g., due to a malformed override). The check is an assertion, not a config hint.
2. **No `hostNetwork`.** The pod-spec builder hard-codes `hostNetwork: false` (it's not configurable). With `hostNetwork: true`, `127.0.0.1:7777` would be node-loopback and reachable from any other pod scheduled on that node — catastrophic.
3. **shareProcessNamespace stays false** for separate reasons (see "Deferred" subsection below).
4. **E2E test.** Phase 4 includes a regression test that: (a) starts the pod, (b) curls `https://<pod>:8443/v0/tasks/gov/vote` with a forged `X-Remote-User: kubernetes-admin` header and no bearer token, (c) asserts 401 from the proxy, (d) curls `http://<pod>:7777` from outside the pod and asserts connection refused.

### SSH-to-K8s trust scope comparison

| Aspect | seienv (today) | sidecar (Phase 1-3) | sidecar (Phase 4) |
|---|---|---|---|
| Who can sign | Anyone with the SSH key | Anyone with network reach to pod:7777 | Anyone whose `aws sso login` → EKS access entry → ClusterRoleBinding chain authorizes `create seinodes/tasks/gov.*` |
| Network blast radius | Reachable from operator subnet | Reachable from any in-cluster pod | TLS terminator at `:8443` requires valid bearer token + SAR-allowed identity; cluster network reach is necessary but far from sufficient |
| Key custody | Operator laptop SSH key + EC2 host keyring | Pod-mounted Secret only | Same (pod-mounted Secret only); operator-side credential is the AWS SSO session, never long-lived |
| Caller attribution | SSH user (`ubuntu`); shared key | None (anonymous) | Full assumed-role ARN from EKS access entry; persisted on the task record and emitted in structured audit logs |
| Audit trail | EC2 `auth.log` + `seid` stdout on host | sidecar engine task store (no caller) | kube-rbac-proxy access log (per request) + sidecar audit log (per mutation) + task store `createdBy` |
| Authz revocation | Rotate the SSH key | n/a | `kubectl delete clusterrolebinding ...` or revoke the SSO permission-set assignment in IAM Identity Center |

**Phase 1-3 ships with comparable trust scope to today** (cluster network reach instead of SSH key). Phase 4 materially improves on every row — most notably revocation, which moves from "rotate the key and redeploy seienv config" to a single `kubectl` command or an SSO console click.

### What kube-rbac-proxy does NOT protect

The proxy gates the sidecar's management HTTP API surface and nothing else. It is not a general pod-perimeter firewall. The following co-tenant ports remain reachable via their own protocols and are explicitly not protected by Phase 4 authn/authz:

| Port | Protocol | Exposed via | Authn at this layer? | Notes |
|---|---|---|---|---|
| `26656` | Tendermint P2P | Service (P2P) | node-key handshake (not human authn) | chain-protocol surface; out of scope |
| `26657` | Tendermint RPC | loopback only | none (binds 127.0.0.1) | not exposed off-pod by default |
| `1317` | Cosmos REST | loopback only | none | not exposed off-pod by default |
| `8545` / `8546` | EVM JSON-RPC / WS | loopback only | none | Sei dual-stack; not exposed off-pod by default |
| `9090` | Cosmos gRPC | loopback only | none | not exposed off-pod by default |
| `26660` | Prometheus metrics | loopback only | none | scraped via in-cluster Prometheus, which has its own auth story |

If an operator exposes any of these off-pod (custom Service, NodePort, etc.), kube-rbac-proxy does NOT protect them. That's a separate decision and a separate authn story (Tendermint RPC has its own JWT module if needed; EVM RPC has nothing built in).

Boundary statement: the proxy exists to gate the management API surface that the sidecar adds to a SeiNode pod — the `/v0/tasks/...` endpoints that submit signed transactions or mutate task state. The chain's own protocols (P2P, RPC, JSON-RPC, gRPC) have their own trust models and are explicitly out of scope for this design. Hardening those surfaces is independent and tracked separately if/when the platform exposes them.

### AWS IAM Identity Center threat model

Phase 4 introduces AWS SSO as the operator-credential issuance path on EKS clusters. Compromise scenarios:

| Scenario | Blast radius | Mitigation |
|---|---|---|
| Compromised operator laptop with active SSO session | Whatever the SSO permission set authorizes, for the remaining session TTL (default 8h). EKS access entry → ClusterRoleBinding gates which K8s identities the role maps to. | SSO session TTL cap; require MFA on SSO; revoke permission-set assignment in IAM Identity Center |
| Compromised IAM Identity Center directory user | The user's permission-set assignments, until directory account is disabled. | Standard IAM Identity Center directory hygiene; out of scope here |
| Compromised AWS root in the cluster account | Total. Attacker can rewrite access entries, mint EKS tokens, edit ClusterRoleBindings. | Out of scope — AWS account compromise is a per-account incident-response problem |
| Compromised K8s admin (`system:masters`) | Total within the cluster. Can sign anything via direct kubectl exec on the sidecar pod. | This is the standard K8s admin trust assumption; Phase 4 doesn't try to defend against it |
| Compromised `kube-rbac-proxy` container image | Per-pod: attacker can fabricate `X-Remote-User` for any incoming request, but is still bound by the sidecar's chain-confusion guard, idempotency dedupe, and keyring presence. Cannot exfiltrate the operator keyring (no mount). | Pin proxy image digest; supply-chain provenance; cosign / sigstore verification at admission time (cluster-level concern) |
| Stolen SSO refresh token (in browser/cookie) | Allows fresh SSO logins until revoked. Same as "compromised laptop with active session" effectively. | Short SSO session TTL; immediate revocation via IAM Identity Center on incident |

The single most important defensive property: **revocation is a single `kubectl delete` or SSO console click**, not a key rotation. This is the structural argument for moving off seienv-style shared SSH keys.

### Container security context (re-stated)

Tightened in Component A as part of Phase 1:

- `runAsNonRoot: true` + `runAsUser: 65532`
- `readOnlyRootFilesystem: true`
- `allowPrivilegeEscalation: false`
- `capabilities.drop: [ALL]`
- `seccompProfile: RuntimeDefault`
- Pod-level `fsGroup: 65532` (required so non-root sidecar can read 0o400 Secret mounts)
- `automountServiceAccountToken: true` (kubelet default; required so the rbac-proxy container can read the projected SA token)

The kube-rbac-proxy container ships with the same security context (Component E).

### Slashing risk

Governance-tx signing is **not** double-sign-able in the consensus sense — voting twice on the same proposal is rejected by the chain (proposer-vote uniqueness), not slashed. The chain-confusion guard prevents cross-chain signature exposure. The chief remaining operational risk is "submit-proposal with wrong upgrade height" which is a manual-input bug, not a protocol-level slashing event. Phase 4's caller attribution makes such operator errors investigable after the fact — the audit log records which SSO-derived identity submitted the bad proposal, so the operator team can triage without guessing.

### Deferred: shareProcessNamespace / sidecar-seid UID separation

`shareProcessNamespace` is currently unset (kubelet default `false`) on SeiNode pods. With it `true`, every container in the pod would see every other container's processes — the sidecar could `ptrace` the seid process or read its memory, which would defeat the entire signing-key/operator-keyring boundary that this design rests on. The runtime guard for "is `shareProcessNamespace` false" is currently implicit (we never set it true); making it an asserted hard-no is tracked in `sei-protocol/sei-k8s-controller#221`.

Related and also deferred to #221: the sidecar and seid containers currently both run with UIDs the pod-spec builder picks (`65532` for the hardened sidecar; whatever the seid image uses for seid main). Aligning these explicitly so kernel-level UID separation is documented and asserted rather than implicit is part of the same workstream.

These items remain Phase 4-adjacent rather than Phase 4-blocking: the kube-rbac-proxy adoption doesn't depend on them, and the loopback-binding trust model holds without them under the current pod composition. They're called out here so the reader understands that "co-tenant container compromise" remains a residual risk dimension that #221 closes separately.

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

Phase 4 (final hardening, ~1-2 weeks; work spans both repos):
- **#165** (seictl + sei-k8s-controller):
  - seictl side: URL path refactor `POST /v0/tasks/<class>/<type>`; sidecar binds `127.0.0.1:7777` when `SEI_SIDECAR_LOOPBACK_ONLY=true`; reads `X-Remote-User` from loopback ingress and records caller attribution on the task; CLI honors HTTPS sidecar URL + bearer token from kubeconfig; removal of pre-authn warning notices.
  - sei-k8s-controller side: `SidecarTLSSpec` CRD field; pod-spec emits the kube-rbac-proxy container with cert-manager-issued TLS; emits the Certificate resource and the rbac-proxy ConfigMap; Service grows the `:8443` port; standard ClusterRoles (`sei-validator-governance-operator`, `sei-validator-lifecycle-controller`) ship in the chart; AWS IAM Identity Center → EKS access entries integration documented.

---

## Alternatives considered

### In-process TokenReview middleware (originally-designed model)

The rev1 design landed authn inside the seictl sidecar binary: an `authMiddleware` wrapped the mux, called `TokenReviews().Create(...)` against the API server, and checked the result against a static comma-separated env-var allowlist (`SEI_AUTHZ_ALLOWED_CALLERS`). Audit logging was an in-process structured log emitter.

**Rejected** in favor of kube-rbac-proxy because:

| Dimension | In-process middleware | kube-rbac-proxy | pods/proxy | APIService | Istio AuthN |
|---|---|---|---|---|---|
| Authn surface | Custom Go code in sidecar | Standard `kube-rbac-proxy` binary | API server proxies (built-in) | Aggregated API server (built-in) | Service mesh |
| Authz surface | Static env-var allowlist | K8s RBAC (ClusterRole/Binding) | K8s RBAC | K8s RBAC | Istio policies (separate) |
| AWS SSO integration | None (would need bespoke work) | Native via EKS access entries | Native (uses API server) | Native | Bespoke |
| `kubectl auth can-i` | No | Yes | Yes | Yes | No |
| Cross-repo coupling | High (every authz change rebuilds sidecar) | Low (chart-managed manifests) | Medium | High | Low |
| Operational maturity | Bespoke | Widely adopted in K8s ecosystem | Built-in but rarely used for app-mgmt APIs | Heavyweight; aggregated-API ceremony | Widely adopted but heavyweight for one API |
| Cluster prerequisites | None | cert-manager | None | None | Istio |
| Per-call cost | 1× TokenReview (in-process cache) | 1× TokenReview + 1× SAR (proxy cache) | 1× TokenReview + 1× SAR at API server | 1× TokenReview + 1× SAR at API server | mTLS handshake |
| Audit trail | sidecar log only | proxy log + sidecar log + K8s audit log | K8s audit log | K8s audit log | Istio telemetry |

kube-rbac-proxy wins on every dimension the rev1 design left to follow-on work: AWS SSO integration, standard RBAC, revocation UX, `kubectl auth can-i` support. The single new prerequisite (cert-manager) is satisfied by every cluster we operate in. Other options were considered and dropped:

- **`pods/proxy`**: routes through the API server's pod proxy. Avoids the cert-manager dep but couples request latency to API server load, doesn't support arbitrary URL shapes cleanly (path encoding gymnastics), and the audit trail is split across the API server's audit log and the sidecar's. Loses on UX (operators have to type `kubectl proxy` flows or use the API server URL directly).
- **APIService aggregation**: heavyweight. Requires running a TLS-fronted API service registered with the aggregator, full apimachinery surface in the sidecar. Worthwhile if we wanted the sidecar to look like a first-class K8s resource type with `kubectl get tasks` semantics; we don't.
- **Istio AuthN**: assumes the cluster runs Istio. We don't mandate Istio, and Phase 4 should not be the wedge that introduces it.

### Removal of `SEI_AUTHZ_*` env vars

The rev1 design wired authz via two env vars on the seictl sidecar:
- `SEI_AUTHZ_ENABLED=true|false`
- `SEI_AUTHZ_ALLOWED_CALLERS=system:serviceaccount:ns:sa,...`

These are deleted in rev2, not deprecated. There is no migration path — the env vars never appeared in a released chart, and rev2 ships before rev1 lands in production. Cluster operators upgrading from a pre-rev2 dev build remove the env-var injection from their controller manifest in the same change that adds `spec.sidecar.tls` to their SeiNodes.

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

Considered: client-cert-based mTLS instead of bearer-token-fronted-by-rbac-proxy.

**Rejected** for v1 because:
- mTLS imposes cert rotation overhead on every operator workstation; SSO bearer tokens have a clean issuance + rotation story via existing AWS SSO + EKS plumbing.
- Bearer-token authn at kube-rbac-proxy is K8s-native and operator-tooling-native (`kubectl create token`, `aws eks get-token`, kubeconfig exec credential plugins all just work).
- mTLS is additively introducible later if a concrete use case demands it (e.g., service-to-service flows where bearer tokens don't fit).

### Per-SeiNode SAs

Considered: controller mints a dedicated SA per SeiNode for blast-radius isolation.

**Deferred** to a future workstream because:
- Phase 4 already separates the proxy SA from the sidecar SA from the controller SA, which is the most operationally relevant isolation.
- Per-SeiNode SAs need their own RBAC machinery in the controller, more cluster-level objects, namespacing decisions.
- Not needed until multi-tenant clusters or per-validator authz becomes a requirement.

### Single Secret for keyring + passphrase

Considered: one Secret with `keyring-file/*.info`, `keyring-file/*.address`, and `passphrase` as siblings; project as volume + extract one key as env.

**Rejected** because:
- Kubernetes Secret volume mounts project ALL data keys as files at the mount path. With one Secret, the passphrase would be projected as a file (`/sei/keyring-file/passphrase`) inside the keyring directory.
- `envFrom: secretRef` projects ALL keys as env vars — the keyring binary blobs would appear in `os.Environ()`.
- Items projection requires enumerating keys at admission time, which the controller cannot do statically.

Two separate Secrets is operationally a small cost; architecturally it preserves the directory-vs-env projection contract cleanly.

---

## Open questions

- **cert-manager mandate vs. self-signed fallback.** Phase 4 currently refuses to enable TLS without a cert-manager `Issuer`. Revisit if airgapped or single-node operator deployments surface as a real use case; the cleanest extension is an `Issuer` of `kind: SelfSignedIssuer` from cert-manager itself, but that still requires cert-manager.
- **IAM Identity Center OIDC integration path.** An alternative to EKS access entries is configuring the cluster to trust IAM Identity Center's OIDC issuer directly. Defer until a concrete cluster setup requires it; current recommendation is Path A (access entries) only.
- **URL path third segment shape.** The current rev2 shape is `/v0/tasks/<class>/<type>` with no per-type verb segment. If a future task family needs `<verb>` (e.g. `gov-vote/dry-run`), the additive escape hatch is a query param `?action=dry-run`, not a fourth path segment — extending the path would require revisiting kube-rbac-proxy's `resourceAttributes` rewrites.
- **AWS IAM Identity Center session TTL recommendation.** IAM Identity Center's default permission-set session is 8 hours. Should the validator-operator permission set cap at 4 hours? Tradeoff is operator UX (re-login frequency) vs. blast-radius window on a compromised laptop. Open for review with the security team.
- **Test backend allowlist.** Should `SEI_KEYRING_BACKEND=test` be allowed in production? Recommendation: refuse if `SEI_CHAIN_ID` matches a known mainnet pattern (e.g., `pacific-1`, `arctic-1`). Implementation detail for #162 reviewers.
- **`UpgradeInfo` semantics for binary handoff.** Sei-specific: does the chain consume `Plan.Info` to download new binaries (e.g., via cosmovisor)? Confirm and document the expected format (URL? structured JSON?) in `docs/gov.md`.
- **Fan-out concurrency model.** The CLI's `--threads` flag is a hard cap; should there be a per-validator timeout to prevent one stuck sidecar from stalling the whole fan-out?

---

## References

### Coral session
This design synthesizes a coral round (2026-05-11) with kubernetes-specialist, blockchain-developer, and platform-engineer specialists; rev2 incorporates a follow-up coral round (2026-05-12) with kubernetes-specialist and security-specialist on the Phase 4 authn architecture.

### GitHub issues
- sei-protocol/sei-k8s-controller#219 — CRD `validator.operatorKeyring`
- sei-protocol/sei-k8s-controller#221 — `shareProcessNamespace` assertion + UID separation (deferred)
- sei-protocol/seictl#162 — Sidecar production keyring backend
- sei-protocol/seictl#163 — Sidecar sign-tx task family
- sei-protocol/seictl#164 — seictl gov CLI subcommands
- sei-protocol/seictl#165 — Sidecar authn via kube-rbac-proxy

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

### External references
- kube-rbac-proxy: https://github.com/brancz/kube-rbac-proxy
- cert-manager: https://cert-manager.io/docs/
- AWS EKS access entries: https://docs.aws.amazon.com/eks/latest/userguide/access-entries.html
- AWS IAM Identity Center: https://docs.aws.amazon.com/singlesignon/latest/userguide/what-is.html
- K8s RBAC documentation: https://kubernetes.io/docs/reference/access-authn-authz/rbac/

---

## Changelog

- 2026-05-11 — Initial draft (coral synthesis)
- 2026-05-12 — rev2: replace in-process TokenReview middleware (Component E) with kube-rbac-proxy sidecar pattern. URL path refactor (`/v0/tasks/<class>/<type>`). Sidecar binds 127.0.0.1; rbac-proxy fronts on 0.0.0.0:8443 with cert-manager-issued TLS. Caller attribution via X-Remote-User headers. AWS IAM Identity Center → EKS access entries → K8s RBAC. shareProcessNamespace gap remains in #221, reasserted in Security posture. SEI_AUTHZ_ENABLED/SEI_AUTHZ_ALLOWED_CALLERS env vars deleted (not deprecated).
