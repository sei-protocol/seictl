package client

import (
	"fmt"
	"net/url"

	"github.com/google/uuid"
	seiconfig "github.com/sei-protocol/sei-config"
	"github.com/sei-protocol/seictl/sidecar/engine"
)

// TaskBuilder is implemented by every typed task struct. It converts a
// strongly-typed task description into the generic TaskRequest wire format
// and validates required fields before submission.
type TaskBuilder interface {
	TaskType() string
	Validate() error
	ToTaskRequest() TaskRequest
}

// TaskMeta carries optional metadata that applies to all task types.
// Embed this in any typed task struct. When ID is non-nil, the sidecar
// uses it as the canonical task identifier (enabling deterministic IDs
// from the controller). When nil, the engine generates a random UUID.
type TaskMeta struct {
	ID *uuid.UUID
}

// applyMeta sets the ID on a TaskRequest if the meta carries one.
func (m TaskMeta) applyMeta(req *TaskRequest) {
	if m.ID != nil {
		req.Id = m.ID
	}
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

	TaskTypeGenerateIdentity       = "generate-identity"
	TaskTypeGenerateGentx          = "generate-gentx"
	TaskTypeUploadGenesisArtifacts = "upload-genesis-artifacts"
	TaskTypeAssembleGenesis        = "assemble-and-upload-genesis"
	TaskTypeSetGenesisPeers        = "set-genesis-peers"
)

// Known condition and action values for AwaitConditionTask.
const (
	ConditionHeight = "height"
	ActionSIGTERM   = "SIGTERM_SEID"
)

// SnapshotRestoreTask downloads and extracts a snapshot archive from S3.
type SnapshotRestoreTask struct {
	TaskMeta
	Bucket  string
	Prefix  string
	Region  string
	ChainID string
}

func (t SnapshotRestoreTask) TaskType() string { return TaskTypeSnapshotRestore }

func (t SnapshotRestoreTask) Validate() error {
	if t.Bucket == "" {
		return fmt.Errorf("snapshot-restore: missing required field Bucket")
	}
	if t.Prefix == "" {
		return fmt.Errorf("snapshot-restore: missing required field Prefix")
	}
	if t.Region == "" {
		return fmt.Errorf("snapshot-restore: missing required field Region")
	}
	if t.ChainID == "" {
		return fmt.Errorf("snapshot-restore: missing required field ChainID")
	}
	return nil
}

func (t SnapshotRestoreTask) ToTaskRequest() TaskRequest {
	p := map[string]interface{}{
		"bucket":  t.Bucket,
		"prefix":  t.Prefix,
		"region":  t.Region,
		"chainId": t.ChainID,
	}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	t.applyMeta(&req)
	return req
}

// SnapshotRestoreTaskFromParams reconstructs a SnapshotRestoreTask from
// a generic params map. Useful for round-trip testing.
func SnapshotRestoreTaskFromParams(params map[string]interface{}) SnapshotRestoreTask {
	s := func(k string) string { v, _ := params[k].(string); return v }
	return SnapshotRestoreTask{
		Bucket:  s("bucket"),
		Prefix:  s("prefix"),
		Region:  s("region"),
		ChainID: s("chainId"),
	}
}

// SnapshotUploadTask archives and streams a local snapshot to S3.
// Schedule may be set to run this task on a recurring cron.
type SnapshotUploadTask struct {
	TaskMeta
	Bucket   string
	Prefix   string
	Region   string
	Schedule *ScheduleConfig
}

func (t SnapshotUploadTask) TaskType() string { return TaskTypeSnapshotUpload }

func (t SnapshotUploadTask) Validate() error {
	if t.Bucket == "" {
		return fmt.Errorf("snapshot-upload: missing required field Bucket")
	}
	if t.Region == "" {
		return fmt.Errorf("snapshot-upload: missing required field Region")
	}
	return nil
}

func (t SnapshotUploadTask) ToTaskRequest() TaskRequest {
	p := map[string]interface{}{
		"bucket": t.Bucket,
		"region": t.Region,
	}
	if t.Prefix != "" {
		p["prefix"] = t.Prefix
	}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	if t.Schedule != nil {
		req.Schedule = t.Schedule
	}
	t.applyMeta(&req)
	return req
}

// SnapshotUploadTaskFromParams reconstructs a SnapshotUploadTask from
// a generic params map.
func SnapshotUploadTaskFromParams(params map[string]interface{}) SnapshotUploadTask {
	s := func(k string) string { v, _ := params[k].(string); return v }
	return SnapshotUploadTask{
		Bucket: s("bucket"),
		Prefix: s("prefix"),
		Region: s("region"),
	}
}

// ConfigureGenesisTask configures genesis.json for a node. When URI and Region
// are set, the sidecar downloads from S3. When they are empty, the sidecar
// falls back to writing the embedded genesis for the chain ID it was started
// with (set via SEI_CHAIN_ID environment variable).
type ConfigureGenesisTask struct {
	TaskMeta
	URI    string
	Region string
}

func (t ConfigureGenesisTask) TaskType() string { return TaskTypeConfigureGenesis }

func (t ConfigureGenesisTask) Validate() error {
	if t.URI == "" {
		return nil
	}
	if t.Region == "" {
		return fmt.Errorf("configure-genesis: Region is required when URI is set")
	}
	parsed, err := url.Parse(t.URI)
	if err != nil {
		return fmt.Errorf("configure-genesis: invalid URI %q: %w", t.URI, err)
	}
	if parsed.Scheme != "s3" {
		return fmt.Errorf("configure-genesis: URI must use s3:// scheme, got %q", parsed.Scheme)
	}
	if parsed.Host == "" || parsed.Path == "" || parsed.Path == "/" {
		return fmt.Errorf("configure-genesis: URI must be s3://bucket/key, got %q", t.URI)
	}
	return nil
}

func (t ConfigureGenesisTask) ToTaskRequest() TaskRequest {
	var req TaskRequest
	if t.URI == "" {
		req = TaskRequest{Type: t.TaskType()}
	} else {
		p := map[string]interface{}{
			"uri":    t.URI,
			"region": t.Region,
		}
		req = TaskRequest{Type: t.TaskType(), Params: &p}
	}
	t.applyMeta(&req)
	return req
}

// ConfigureGenesisTaskFromParams reconstructs a ConfigureGenesisTask from
// a generic params map.
func ConfigureGenesisTaskFromParams(params map[string]interface{}) ConfigureGenesisTask {
	s := func(k string) string { v, _ := params[k].(string); return v }
	return ConfigureGenesisTask{
		URI:    s("uri"),
		Region: s("region"),
	}
}

// PeerSourceType identifies the peer discovery mechanism.
type PeerSourceType string

const (
	PeerSourceEC2Tags PeerSourceType = "ec2Tags"
	PeerSourceStatic  PeerSourceType = "static"
)

// PeerSource is a single peer discovery source.
type PeerSource struct {
	Type      PeerSourceType
	Region    string
	Tags      map[string]string
	Addresses []string
}

// DiscoverPeersTask resolves peers from one or more sources.
type DiscoverPeersTask struct {
	TaskMeta
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
		}
		sources[i] = m
	}
	p := map[string]interface{}{"sources": sources}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	t.applyMeta(&req)
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
	TaskMeta
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
	t.applyMeta(&req)
	return req
}

// ConfigureStateSyncTask discovers a trust point and configures state sync.
// When UseLocalSnapshot is true, the task uses the locally-restored snapshot
// height as the trust height and sets use-local-snapshot = true in config.toml.
type ConfigureStateSyncTask struct {
	TaskMeta
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
	t.applyMeta(&req)
	return req
}

// MarkReadyTask signals that bootstrap is complete.
type MarkReadyTask struct{ TaskMeta }

func (t MarkReadyTask) TaskType() string { return TaskTypeMarkReady }
func (t MarkReadyTask) Validate() error  { return nil }

func (t MarkReadyTask) ToTaskRequest() TaskRequest {
	req := TaskRequest{Type: t.TaskType()}
	t.applyMeta(&req)
	return req
}

// ConfigApplyTask generates or patches node config using sei-config's
// intent resolution pipeline. The caller builds a ConfigIntent describing
// the desired state; the sidecar resolves it via sei-config.
type ConfigApplyTask struct {
	TaskMeta
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
	t.applyMeta(&req)
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
type ConfigValidateTask struct{ TaskMeta }

func (t ConfigValidateTask) TaskType() string { return TaskTypeConfigValidate }
func (t ConfigValidateTask) Validate() error  { return nil }

func (t ConfigValidateTask) ToTaskRequest() TaskRequest {
	req := TaskRequest{Type: t.TaskType()}
	t.applyMeta(&req)
	return req
}

// ConfigReloadTask patches hot-reloadable fields on disk and signals seid
// to re-read its configuration.
type ConfigReloadTask struct {
	TaskMeta
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
	t.applyMeta(&req)
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
// them in paginated NDJSON files to S3.
// Schedule may be set to run this task on a recurring cron.
type ResultExportTask struct {
	TaskMeta
	Bucket   string
	Prefix   string
	Region   string
	Schedule *ScheduleConfig
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
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	if t.Schedule != nil {
		req.Schedule = t.Schedule
	}
	t.applyMeta(&req)
	return req
}

// ResultExportTaskFromParams reconstructs a ResultExportTask from
// a generic params map.
func ResultExportTaskFromParams(params map[string]interface{}) ResultExportTask {
	s := func(k string) string { v, _ := params[k].(string); return v }
	return ResultExportTask{
		Bucket: s("bucket"),
		Prefix: s("prefix"),
		Region: s("region"),
	}
}

// GenerateIdentityTask creates validator identity (keys, node ID).
type GenerateIdentityTask struct {
	TaskMeta
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
	t.applyMeta(&req)
	return req
}

// GenerateGentxTask creates a gentx for the validator. The handler discovers
// the node's own account address from the keys generated during identity
// creation and funds it with AccountBalance before generating the gentx.
type GenerateGentxTask struct {
	TaskMeta
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
	t.applyMeta(&req)
	return req
}

// UploadGenesisArtifactsTask uploads identity.json and gentx.json to S3.
type UploadGenesisArtifactsTask struct {
	TaskMeta
	S3Bucket string
	S3Prefix string
	S3Region string
	NodeName string
}

func (t UploadGenesisArtifactsTask) TaskType() string { return TaskTypeUploadGenesisArtifacts }

func (t UploadGenesisArtifactsTask) Validate() error {
	if t.S3Bucket == "" {
		return fmt.Errorf("upload-genesis-artifacts: missing required field S3Bucket")
	}
	if t.S3Region == "" {
		return fmt.Errorf("upload-genesis-artifacts: missing required field S3Region")
	}
	if t.NodeName == "" {
		return fmt.Errorf("upload-genesis-artifacts: missing required field NodeName")
	}
	return nil
}

func (t UploadGenesisArtifactsTask) ToTaskRequest() TaskRequest {
	p := map[string]interface{}{
		"s3Bucket": t.S3Bucket,
		"s3Prefix": t.S3Prefix,
		"s3Region": t.S3Region,
		"nodeName": t.NodeName,
	}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	t.applyMeta(&req)
	return req
}

// GenesisNodeParam is the wire format for nodes[] in assemble-and-upload-genesis.
type GenesisNodeParam struct {
	Name string `json:"name"`
}

// AssembleAndUploadGenesisTask collects per-node artifacts and produces final genesis.json.
type AssembleAndUploadGenesisTask struct {
	TaskMeta
	S3Bucket string
	S3Prefix string
	S3Region string
	ChainID  string
	Nodes    []GenesisNodeParam
}

func (t AssembleAndUploadGenesisTask) TaskType() string { return TaskTypeAssembleGenesis }

func (t AssembleAndUploadGenesisTask) Validate() error {
	if t.S3Bucket == "" {
		return fmt.Errorf("assemble-and-upload-genesis: missing required field S3Bucket")
	}
	if t.S3Region == "" {
		return fmt.Errorf("assemble-and-upload-genesis: missing required field S3Region")
	}
	if t.ChainID == "" {
		return fmt.Errorf("assemble-and-upload-genesis: missing required field ChainID")
	}
	if len(t.Nodes) == 0 {
		return fmt.Errorf("assemble-and-upload-genesis: at least one node is required")
	}
	return nil
}

func (t AssembleAndUploadGenesisTask) ToTaskRequest() TaskRequest {
	nodes := make([]interface{}, len(t.Nodes))
	for i, n := range t.Nodes {
		nodes[i] = map[string]interface{}{"name": n.Name}
	}
	p := map[string]interface{}{
		"s3Bucket": t.S3Bucket,
		"s3Prefix": t.S3Prefix,
		"s3Region": t.S3Region,
		"chainId":  t.ChainID,
		"nodes":    nodes,
	}
	req := TaskRequest{Type: t.TaskType(), Params: &p}
	t.applyMeta(&req)
	return req
}

// AwaitConditionTask blocks until a condition is met, then optionally
// executes a post-condition action. Currently supports the "height"
// condition and the "SIGTERM_SEID" action.
type AwaitConditionTask struct {
	TaskMeta
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
	t.applyMeta(&req)
	return req
}
