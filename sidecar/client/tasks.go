package client

import (
	"errors"
	"fmt"

	"github.com/cosmos/btcutil/bech32"
	seiconfig "github.com/sei-protocol/sei-config"
	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seictl/sidecar/tasks"
)

const seiBech32HRP = "sei"

// btcutil's bech32 decode validates checksum + alphabet; cheaper than
// pulling sei-cosmos's sdk.AccAddressFromBech32 just for client-side
// validation. Sidecar still does the SDK-shape check server-side.
func validateSeiAccountAddress(addr string) error {
	hrp, _, err := bech32.Decode(addr, 1023) // bech32 spec max length
	if err != nil {
		return fmt.Errorf("address %q: %w", addr, err)
	}
	if hrp != seiBech32HRP {
		return fmt.Errorf("address %q: hrp %q, expected %q", addr, hrp, seiBech32HRP)
	}
	return nil
}

// TaskBuilder is implemented by every typed task struct. It converts a
// strongly-typed task description into the generic TaskRequest wire format
// and validates required fields before submission.
type TaskBuilder interface {
	TaskType() string
	Validate() error
	ToTaskRequest() TaskRequest
}

// Task type constants re-exported from engine for external consumers.
const (
	TaskTypeSnapshotRestore    = string(engine.TaskSnapshotRestore)
	TaskTypeDiscoverPeers      = string(engine.TaskDiscoverPeers)
	TaskTypeConfigPatch        = string(engine.TaskConfigPatch)
	TaskTypeConfigApply        = string(engine.TaskConfigApply)
	TaskTypeConfigValidate     = string(engine.TaskConfigValidate)
	TaskTypeConfigReload       = string(engine.TaskConfigReload)
	TaskTypeMarkReady          = string(engine.TaskMarkReady)
	TaskTypeConfigureGenesis   = string(engine.TaskConfigureGenesis)
	TaskTypeConfigureStateSync = string(engine.TaskConfigureStateSync)
	TaskTypeSnapshotUpload     = string(engine.TaskSnapshotUpload)
	TaskTypeResultExport       = string(engine.TaskResultExport)
	TaskTypeAwaitCondition     = string(engine.TaskAwaitCondition)

	TaskTypeGenerateIdentity       = string(engine.TaskGenerateIdentity)
	TaskTypeGenerateGentx          = string(engine.TaskGenerateGentx)
	TaskTypeUploadGenesisArtifacts = string(engine.TaskUploadGenesisArtifacts)
	TaskTypeAssembleGenesis        = string(engine.TaskAssembleAndUploadGenesis)
	TaskTypeSetGenesisPeers        = string(engine.TaskSetGenesisPeers)

	TaskTypeGovVote            = string(engine.TaskGovVote)
	TaskTypeGovSoftwareUpgrade = string(engine.TaskGovSoftwareUpgrade)
)

// Known condition and action values for AwaitConditionTask.
const (
	ConditionHeight = "height"
	ActionSIGTERM   = "SIGTERM_SEID"
)

// SnapshotRestoreTask downloads and extracts a snapshot archive from S3.
// S3 coordinates are derived by the sidecar from its environment.
// TargetHeight selects the highest available snapshot <= that height.
// When zero, the latest snapshot (from latest.txt) is used.
type SnapshotRestoreTask struct {
	TargetHeight int64
}

func (t SnapshotRestoreTask) TaskType() string { return TaskTypeSnapshotRestore }

func (t SnapshotRestoreTask) Validate() error { return nil }

func (t SnapshotRestoreTask) ToTaskRequest() TaskRequest {
	var p *map[string]interface{}
	if t.TargetHeight > 0 {
		m := map[string]interface{}{"targetHeight": t.TargetHeight}
		p = &m
	}
	req := TaskRequest{Type: t.TaskType(), Params: p}
	return req
}

// SnapshotRestoreTaskFromParams reconstructs a SnapshotRestoreTask from
// a generic params map. Useful for round-trip testing.
func SnapshotRestoreTaskFromParams(params map[string]interface{}) SnapshotRestoreTask {
	var t SnapshotRestoreTask
	switch h := params["targetHeight"].(type) {
	case float64:
		t.TargetHeight = int64(h)
	case int64:
		t.TargetHeight = h
	}
	return t
}

// SnapshotUploadTask archives and streams a local snapshot to S3.
// S3 coordinates are derived by the sidecar from its environment.
type SnapshotUploadTask struct {
}

func (t SnapshotUploadTask) TaskType() string { return TaskTypeSnapshotUpload }

func (t SnapshotUploadTask) Validate() error { return nil }

func (t SnapshotUploadTask) ToTaskRequest() TaskRequest {
	req := TaskRequest{Type: t.TaskType()}
	return req
}

// SnapshotUploadTaskFromParams reconstructs a SnapshotUploadTask from
// a generic params map.
func SnapshotUploadTaskFromParams(_ map[string]interface{}) SnapshotUploadTask {
	return SnapshotUploadTask{}
}

// ConfigureGenesisTask instructs the sidecar to resolve and write genesis.json.
// The sidecar resolves genesis from its chain ID: embedded config is checked
// first, then S3 fallback at {bucket}/{chainID}/genesis.json using env vars.
// No parameters are needed from the controller.
type ConfigureGenesisTask struct {
}

func (t ConfigureGenesisTask) TaskType() string { return TaskTypeConfigureGenesis }

func (t ConfigureGenesisTask) Validate() error { return nil }

func (t ConfigureGenesisTask) ToTaskRequest() TaskRequest {
	req := TaskRequest{Type: t.TaskType()}
	return req
}

// ConfigureGenesisTaskFromParams reconstructs a ConfigureGenesisTask from
// a generic params map.
func ConfigureGenesisTaskFromParams(_ map[string]interface{}) ConfigureGenesisTask {
	return ConfigureGenesisTask{}
}

// PeerSourceType identifies the peer discovery mechanism.
type PeerSourceType string

const (
	PeerSourceEC2Tags      PeerSourceType = "ec2Tags"
	PeerSourceStatic       PeerSourceType = "static"
	PeerSourceDNSEndpoints PeerSourceType = "dnsEndpoints"
)

// PeerSource is a single peer discovery source.
type PeerSource struct {
	Type      PeerSourceType    `json:"type"`
	Region    string            `json:"region,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
	Addresses []string          `json:"addresses,omitempty"`
	Endpoints []string          `json:"endpoints,omitempty"`
}

// DiscoverPeersTask resolves peers from one or more sources.
type DiscoverPeersTask struct {
	Sources []PeerSource
}

func (t DiscoverPeersTask) TaskType() string { return TaskTypeDiscoverPeers }

func (t DiscoverPeersTask) Validate() error {
	if len(t.Sources) == 0 {
		return fmt.Errorf("discover-peers: at least one source is required")
	}
	for i, src := range t.Sources {
		switch src.Type {
		case PeerSourceEC2Tags:
			if src.Region == "" {
				return fmt.Errorf("discover-peers: source[%d] ec2Tags missing required field Region", i)
			}
			if len(src.Tags) == 0 {
				return fmt.Errorf("discover-peers: source[%d] ec2Tags missing required field Tags", i)
			}
		case PeerSourceStatic:
			if len(src.Addresses) == 0 {
				return fmt.Errorf("discover-peers: source[%d] static missing required field Addresses", i)
			}
		case PeerSourceDNSEndpoints:
			if len(src.Endpoints) == 0 {
				return fmt.Errorf("discover-peers: source[%d] dnsEndpoints missing required field Endpoints", i)
			}
		default:
			return fmt.Errorf("discover-peers: source[%d] unknown type %q", i, src.Type)
		}
	}
	return nil
}

func (t DiscoverPeersTask) ToTaskRequest() TaskRequest {
	sources := make([]interface{}, len(t.Sources))
	for i, src := range t.Sources {
		m := map[string]interface{}{"type": string(src.Type)}
		switch src.Type {
		case PeerSourceEC2Tags:
			m["region"] = src.Region
			tags := make(map[string]interface{}, len(src.Tags))
			for k, v := range src.Tags {
				tags[k] = v
			}
			m["tags"] = tags
		case PeerSourceStatic:
			addrs := make([]interface{}, len(src.Addresses))
			for j, a := range src.Addresses {
				addrs[j] = a
			}
			m["addresses"] = addrs
		case PeerSourceDNSEndpoints:
			eps := make([]interface{}, len(src.Endpoints))
			for j, e := range src.Endpoints {
				eps[j] = e
			}
			m["endpoints"] = eps
		}
		sources[i] = m
	}
	p := map[string]interface{}{"sources": sources}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	return req
}

// DiscoverPeersTaskFromParams reconstructs a DiscoverPeersTask from
// a generic params map.
func DiscoverPeersTaskFromParams(params map[string]interface{}) (DiscoverPeersTask, error) {
	rawSources, ok := params["sources"]
	if !ok {
		return DiscoverPeersTask{}, fmt.Errorf("missing 'sources' key")
	}
	items, ok := rawSources.([]interface{})
	if !ok {
		return DiscoverPeersTask{}, fmt.Errorf("sources is not a list")
	}

	var sources []PeerSource
	for i, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			return DiscoverPeersTask{}, fmt.Errorf("source[%d] is not an object", i)
		}
		typ, _ := m["type"].(string)
		src := PeerSource{Type: PeerSourceType(typ)}

		switch PeerSourceType(typ) {
		case PeerSourceEC2Tags:
			src.Region, _ = m["region"].(string)
			if rawTags, ok := m["tags"].(map[string]interface{}); ok {
				src.Tags = make(map[string]string, len(rawTags))
				for k, v := range rawTags {
					src.Tags[k], _ = v.(string)
				}
			}
		case PeerSourceStatic:
			if rawAddrs, ok := m["addresses"].([]interface{}); ok {
				src.Addresses = make([]string, 0, len(rawAddrs))
				for _, a := range rawAddrs {
					if s, ok := a.(string); ok {
						src.Addresses = append(src.Addresses, s)
					}
				}
			}
		case PeerSourceDNSEndpoints:
			if rawEps, ok := m["endpoints"].([]interface{}); ok {
				src.Endpoints = make([]string, 0, len(rawEps))
				for _, e := range rawEps {
					if s, ok := e.(string); ok {
						src.Endpoints = append(src.Endpoints, s)
					}
				}
			}
		default:
			return DiscoverPeersTask{}, fmt.Errorf("source[%d] unknown type %q", i, typ)
		}
		sources = append(sources, src)
	}
	return DiscoverPeersTask{Sources: sources}, nil
}

// ConfigPatchTask applies generic TOML merge-patches to seid config files.
// Files maps a filename (e.g. "config.toml", "app.toml") to a nested patch
// that will be recursively merged into the existing file.
type ConfigPatchTask struct {
	Files map[string]map[string]any
}

func (t ConfigPatchTask) TaskType() string { return TaskTypeConfigPatch }

func (t ConfigPatchTask) Validate() error {
	if len(t.Files) == 0 {
		return fmt.Errorf("config-patch: at least one file is required")
	}
	return nil
}

func (t ConfigPatchTask) ToTaskRequest() TaskRequest {
	files := make(map[string]interface{}, len(t.Files))
	for k, v := range t.Files {
		files[k] = v
	}
	p := map[string]interface{}{"files": files}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	return req
}

// ConfigureStateSyncTask discovers a trust point and configures state sync.
// When UseLocalSnapshot is true, the task uses the locally-restored snapshot
// height as the trust height and sets use-local-snapshot = true in config.toml.
type ConfigureStateSyncTask struct {
	UseLocalSnapshot bool
	TrustPeriod      string
	BackfillBlocks   int64
}

func (t ConfigureStateSyncTask) TaskType() string { return TaskTypeConfigureStateSync }
func (t ConfigureStateSyncTask) Validate() error  { return nil }

func (t ConfigureStateSyncTask) ToTaskRequest() TaskRequest {
	p := map[string]interface{}{}
	if t.UseLocalSnapshot {
		p["useLocalSnapshot"] = true
	}
	if t.TrustPeriod != "" {
		p["trustPeriod"] = t.TrustPeriod
	}
	if t.BackfillBlocks > 0 {
		p["backfillBlocks"] = t.BackfillBlocks
	}
	var req TaskRequest
	if len(p) == 0 {
		req = TaskRequest{Type: t.TaskType()}
	} else {
		req = TaskRequest{Type: t.TaskType(), Params: &p}
	}
	return req
}

// MarkReadyTask signals that bootstrap is complete.
type MarkReadyTask struct{}

func (t MarkReadyTask) TaskType() string { return TaskTypeMarkReady }
func (t MarkReadyTask) Validate() error  { return nil }

func (t MarkReadyTask) ToTaskRequest() TaskRequest {
	req := TaskRequest{Type: t.TaskType()}
	return req
}

// SetGenesisPeersTask requests the sidecar to publish this node's peer
// entry to the shared genesis peers list (S3 coordinates derived from
// the sidecar environment).
type SetGenesisPeersTask struct{}

func (t SetGenesisPeersTask) TaskType() string { return TaskTypeSetGenesisPeers }
func (t SetGenesisPeersTask) Validate() error  { return nil }

func (t SetGenesisPeersTask) ToTaskRequest() TaskRequest {
	req := TaskRequest{Type: t.TaskType()}
	return req
}

// ConfigApplyTask generates or patches node config using sei-config's
// intent resolution pipeline. The caller builds a ConfigIntent describing
// the desired state; the sidecar resolves it via sei-config.
type ConfigApplyTask struct {
	Intent seiconfig.ConfigIntent
}

func (t ConfigApplyTask) TaskType() string { return TaskTypeConfigApply }

func (t ConfigApplyTask) Validate() error {
	result := seiconfig.ValidateIntent(t.Intent)
	if !result.Valid {
		return fmt.Errorf("config-apply: invalid intent: %v", result.Diagnostics)
	}
	return nil
}

func (t ConfigApplyTask) ToTaskRequest() TaskRequest {
	p := map[string]interface{}{
		"mode":          string(t.Intent.Mode),
		"incremental":   t.Intent.Incremental,
		"targetVersion": t.Intent.TargetVersion,
	}
	if len(t.Intent.Overrides) > 0 {
		overrides := make(map[string]interface{}, len(t.Intent.Overrides))
		for k, v := range t.Intent.Overrides {
			overrides[k] = v
		}
		p["overrides"] = overrides
	}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	return req
}

// ConfigApplyTaskFromParams reconstructs a ConfigApplyTask from a generic
// params map.
func ConfigApplyTaskFromParams(params map[string]interface{}) ConfigApplyTask {
	s := func(k string) string { v, _ := params[k].(string); return v }
	inc, _ := params["incremental"].(bool)
	tv := 0
	if raw, ok := params["targetVersion"].(float64); ok {
		tv = int(raw)
	}
	var overrides map[string]string
	if raw, ok := params["overrides"].(map[string]interface{}); ok {
		overrides = make(map[string]string, len(raw))
		for k, v := range raw {
			overrides[k], _ = v.(string)
		}
	}
	return ConfigApplyTask{
		Intent: seiconfig.ConfigIntent{
			Mode:          seiconfig.NodeMode(s("mode")),
			Overrides:     overrides,
			Incremental:   inc,
			TargetVersion: tv,
		},
	}
}

// ConfigValidateTask reads on-disk config and returns validation diagnostics.
type ConfigValidateTask struct{}

func (t ConfigValidateTask) TaskType() string { return TaskTypeConfigValidate }
func (t ConfigValidateTask) Validate() error  { return nil }

func (t ConfigValidateTask) ToTaskRequest() TaskRequest {
	req := TaskRequest{Type: t.TaskType()}
	return req
}

// ConfigReloadTask patches hot-reloadable fields on disk and signals seid
// to re-read its configuration.
type ConfigReloadTask struct {
	Fields map[string]string
}

func (t ConfigReloadTask) TaskType() string { return TaskTypeConfigReload }

func (t ConfigReloadTask) Validate() error {
	if len(t.Fields) == 0 {
		return fmt.Errorf("config-reload: at least one field is required")
	}
	return nil
}

func (t ConfigReloadTask) ToTaskRequest() TaskRequest {
	fields := make(map[string]interface{}, len(t.Fields))
	for k, v := range t.Fields {
		fields[k] = v
	}
	p := map[string]interface{}{"fields": fields}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	return req
}

// ConfigReloadTaskFromParams reconstructs a ConfigReloadTask from a generic
// params map.
func ConfigReloadTaskFromParams(params map[string]interface{}) ConfigReloadTask {
	var fields map[string]string
	if raw, ok := params["fields"].(map[string]interface{}); ok {
		fields = make(map[string]string, len(raw))
		for k, v := range raw {
			fields[k], _ = v.(string)
		}
	}
	return ConfigReloadTask{Fields: fields}
}

// ResultExportTask queries the local seid RPC for block results and uploads
// them in paginated NDJSON files to S3. CanonicalRPC enables comparison mode:
// the sidecar compares local block results against the canonical chain and
// the task completes when app-hash divergence is detected.
type ResultExportTask struct {
	Bucket       string
	Prefix       string
	Region       string
	CanonicalRPC string
}

func (t ResultExportTask) TaskType() string { return TaskTypeResultExport }

func (t ResultExportTask) Validate() error {
	if t.Bucket == "" {
		return fmt.Errorf("result-export: missing required field Bucket")
	}
	if t.Region == "" {
		return fmt.Errorf("result-export: missing required field Region")
	}
	return nil
}

func (t ResultExportTask) ToTaskRequest() TaskRequest {
	p := map[string]interface{}{
		"bucket": t.Bucket,
		"region": t.Region,
	}
	if t.Prefix != "" {
		p["prefix"] = t.Prefix
	}
	if t.CanonicalRPC != "" {
		p["canonicalRpc"] = t.CanonicalRPC
	}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	return req
}

// ResultExportTaskFromParams reconstructs a ResultExportTask from
// a generic params map.
func ResultExportTaskFromParams(params map[string]interface{}) ResultExportTask {
	s := func(k string) string { v, _ := params[k].(string); return v }
	return ResultExportTask{
		Bucket:       s("bucket"),
		Prefix:       s("prefix"),
		Region:       s("region"),
		CanonicalRPC: s("canonicalRpc"),
	}
}

// GenerateIdentityTask creates validator identity (keys, node ID).
type GenerateIdentityTask struct {
	ChainID string
	Moniker string
}

func (t GenerateIdentityTask) TaskType() string { return TaskTypeGenerateIdentity }

func (t GenerateIdentityTask) Validate() error {
	if t.ChainID == "" {
		return fmt.Errorf("generate-identity: missing required field ChainID")
	}
	if t.Moniker == "" {
		return fmt.Errorf("generate-identity: missing required field Moniker")
	}
	return nil
}

func (t GenerateIdentityTask) ToTaskRequest() TaskRequest {
	p := map[string]interface{}{
		"chainId": t.ChainID,
		"moniker": t.Moniker,
	}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	return req
}

// GenerateGentxTask creates a gentx for the validator. The handler discovers
// the node's own account address from the keys generated during identity
// creation and funds it with AccountBalance before generating the gentx.
type GenerateGentxTask struct {
	ChainID        string
	StakingAmount  string
	AccountBalance string
	GenesisParams  string
}

func (t GenerateGentxTask) TaskType() string { return TaskTypeGenerateGentx }

func (t GenerateGentxTask) Validate() error {
	if t.ChainID == "" {
		return fmt.Errorf("generate-gentx: missing required field ChainID")
	}
	if t.StakingAmount == "" {
		return fmt.Errorf("generate-gentx: missing required field StakingAmount")
	}
	if t.AccountBalance == "" {
		return fmt.Errorf("generate-gentx: missing required field AccountBalance")
	}
	return nil
}

func (t GenerateGentxTask) ToTaskRequest() TaskRequest {
	p := map[string]interface{}{
		"chainId":        t.ChainID,
		"stakingAmount":  t.StakingAmount,
		"accountBalance": t.AccountBalance,
	}
	if t.GenesisParams != "" {
		p["genesisParams"] = t.GenesisParams
	}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	return req
}

// UploadGenesisArtifactsTask uploads identity.json and gentx.json to S3.
// S3 coordinates are derived by the sidecar from its environment.
type UploadGenesisArtifactsTask struct {
	NodeName string
}

func (t UploadGenesisArtifactsTask) TaskType() string { return TaskTypeUploadGenesisArtifacts }

func (t UploadGenesisArtifactsTask) Validate() error {
	if t.NodeName == "" {
		return fmt.Errorf("upload-genesis-artifacts: missing required field NodeName")
	}
	return nil
}

func (t UploadGenesisArtifactsTask) ToTaskRequest() TaskRequest {
	p := map[string]interface{}{
		"nodeName": t.NodeName,
	}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	return req
}

// GenesisNodeParam is the wire format for nodes[] in assemble-and-upload-genesis.
type GenesisNodeParam struct {
	Name string `json:"name"`
}

// GenesisAccountEntry mirrors SeiNodeDeployment.spec.genesis.accounts[]
// on the controller-CRD side.
type GenesisAccountEntry struct {
	Address string `json:"address"`
	Balance string `json:"balance"`
}

func genesisAccountsToWire(accounts []GenesisAccountEntry) []interface{} {
	if len(accounts) == 0 {
		return nil
	}
	out := make([]interface{}, len(accounts))
	for i, a := range accounts {
		out[i] = map[string]interface{}{"address": a.Address, "balance": a.Balance}
	}
	return out
}

// Balance shape is validated server-side via sdk.ParseCoinsNormalized.
func validateGenesisAccounts(prefix string, accounts []GenesisAccountEntry) error {
	for i, a := range accounts {
		if a.Address == "" {
			return fmt.Errorf("%s: accounts[%d] missing required field Address", prefix, i)
		}
		if a.Balance == "" {
			return fmt.Errorf("%s: accounts[%d] missing required field Balance", prefix, i)
		}
		if err := validateSeiAccountAddress(a.Address); err != nil {
			return fmt.Errorf("%s: accounts[%d]: %w", prefix, i, err)
		}
	}
	return nil
}

// AssembleAndUploadGenesisTask collects per-node artifacts and produces final genesis.json.
// S3 coordinates are derived by the sidecar from its environment.
type AssembleAndUploadGenesisTask struct {
	AccountBalance string
	Namespace      string
	Nodes          []GenesisNodeParam
	Accounts       []GenesisAccountEntry
}

func (t AssembleAndUploadGenesisTask) TaskType() string { return TaskTypeAssembleGenesis }

func (t AssembleAndUploadGenesisTask) Validate() error {
	if t.AccountBalance == "" {
		return fmt.Errorf("assemble-and-upload-genesis: missing required field AccountBalance")
	}
	if t.Namespace == "" {
		return fmt.Errorf("assemble-and-upload-genesis: missing required field Namespace")
	}
	if len(t.Nodes) == 0 {
		return fmt.Errorf("assemble-and-upload-genesis: at least one node is required")
	}
	return validateGenesisAccounts("assemble-and-upload-genesis", t.Accounts)
}

func (t AssembleAndUploadGenesisTask) ToTaskRequest() TaskRequest {
	nodes := make([]interface{}, len(t.Nodes))
	for i, n := range t.Nodes {
		nodes[i] = map[string]interface{}{"name": n.Name}
	}
	p := map[string]interface{}{
		"accountBalance": t.AccountBalance,
		"namespace":      t.Namespace,
		"nodes":          nodes,
	}
	if accounts := genesisAccountsToWire(t.Accounts); accounts != nil {
		p["accounts"] = accounts
	}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	return req
}

// AwaitConditionTask blocks until a condition is met, then optionally
// executes a post-condition action. Currently supports the "height"
// condition and the "SIGTERM_SEID" action.
type AwaitConditionTask struct {
	Condition    string
	TargetHeight int64
	Action       string
}

func (t AwaitConditionTask) TaskType() string { return TaskTypeAwaitCondition }

func (t AwaitConditionTask) Validate() error {
	switch t.Condition {
	case ConditionHeight:
		if t.TargetHeight <= 0 {
			return fmt.Errorf("await-condition: height condition requires TargetHeight > 0")
		}
	default:
		return fmt.Errorf("await-condition: unknown condition %q", t.Condition)
	}
	if t.Action != "" && t.Action != ActionSIGTERM {
		return fmt.Errorf("await-condition: unknown action %q", t.Action)
	}
	return nil
}

func (t AwaitConditionTask) ToTaskRequest() TaskRequest {
	p := map[string]interface{}{
		"condition":    t.Condition,
		"targetHeight": t.TargetHeight,
	}
	if t.Action != "" {
		p["action"] = t.Action
	}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	return req
}

// GovVoteTask submits a gov v1beta1 vote.
type GovVoteTask struct {
	ChainID    string
	KeyName    string
	ProposalID uint64
	Option     string // yes | no | abstain | no_with_veto
	Memo       string
	Fees       string
	Gas        uint64
}

func (t GovVoteTask) TaskType() string { return TaskTypeGovVote }

func (t GovVoteTask) Validate() error {
	if t.ChainID == "" {
		return errors.New("gov-vote: chainId required")
	}
	if t.KeyName == "" {
		return errors.New("gov-vote: keyName required")
	}
	if t.ProposalID == 0 {
		return errors.New("gov-vote: proposalId required (must be > 0)")
	}
	if _, err := tasks.ParseVoteOption(t.Option); err != nil {
		return fmt.Errorf("gov-vote: %w", err)
	}
	if t.Fees == "" {
		return errors.New("gov-vote: fees required")
	}
	if t.Gas == 0 {
		return errors.New("gov-vote: gas required (must be > 0)")
	}
	return nil
}

func (t GovVoteTask) ToTaskRequest() TaskRequest {
	p := map[string]interface{}{
		"chainId":    t.ChainID,
		"keyName":    t.KeyName,
		"proposalId": t.ProposalID,
		"option":     t.Option,
		"fees":       t.Fees,
		"gas":        t.Gas,
	}
	if t.Memo != "" {
		p["memo"] = t.Memo
	}
	return TaskRequest{Type: t.TaskType(), Params: &p}
}

// GovSoftwareUpgradeTask submits a gov v1beta1 software-upgrade
// proposal. The chain auto-assigns proposalID; it is not returned via
// the task result (operators correlate via the memo's taskID= tag).
//
// REHYDRATION WARNING: MsgSubmitProposal is NOT chain-idempotent. See
// the handler doc in sidecar/tasks/gov_software_upgrade.go and #174.
type GovSoftwareUpgradeTask struct {
	ChainID string
	KeyName string

	Title       string
	Description string

	UpgradeName   string
	UpgradeHeight int64
	UpgradeInfo   string

	InitialDeposit string

	Memo string
	Fees string
	Gas  uint64
}

func (t GovSoftwareUpgradeTask) TaskType() string { return TaskTypeGovSoftwareUpgrade }

func (t GovSoftwareUpgradeTask) Validate() error {
	if t.ChainID == "" {
		return errors.New("gov-software-upgrade: chainId required")
	}
	if t.KeyName == "" {
		return errors.New("gov-software-upgrade: keyName required")
	}
	if t.Title == "" {
		return errors.New("gov-software-upgrade: title required")
	}
	if t.Description == "" {
		return errors.New("gov-software-upgrade: description required")
	}
	if t.UpgradeName == "" {
		return errors.New("gov-software-upgrade: upgradeName required")
	}
	if t.UpgradeHeight <= 0 {
		return errors.New("gov-software-upgrade: upgradeHeight required (must be > 0)")
	}
	if t.InitialDeposit == "" {
		return errors.New("gov-software-upgrade: initialDeposit required")
	}
	if t.Fees == "" {
		return errors.New("gov-software-upgrade: fees required")
	}
	if t.Gas == 0 {
		return errors.New("gov-software-upgrade: gas required (must be > 0)")
	}
	return nil
}

func (t GovSoftwareUpgradeTask) ToTaskRequest() TaskRequest {
	p := map[string]interface{}{
		"chainId":        t.ChainID,
		"keyName":        t.KeyName,
		"title":          t.Title,
		"description":    t.Description,
		"upgradeName":    t.UpgradeName,
		"upgradeHeight":  t.UpgradeHeight,
		"initialDeposit": t.InitialDeposit,
		"fees":           t.Fees,
		"gas":            t.Gas,
	}
	if t.UpgradeInfo != "" {
		p["upgradeInfo"] = t.UpgradeInfo
	}
	if t.Memo != "" {
		p["memo"] = t.Memo
	}
	return TaskRequest{Type: t.TaskType(), Params: &p}
}
