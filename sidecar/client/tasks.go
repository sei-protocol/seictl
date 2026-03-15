package client

import (
	"fmt"
	"net/url"
	"time"

	"github.com/robfig/cron/v3"
	seiconfig "github.com/sei-protocol/sei-config"
)

// TaskBuilder is implemented by every typed task struct. It converts a
// strongly-typed task description into the generic TaskRequest wire format
// and validates required fields before submission.
type TaskBuilder interface {
	TaskType() string
	Validate() error
	ToTaskRequest() TaskRequest
}

const (
	TaskTypeSnapshotRestore    = "snapshot-restore"
	TaskTypeDiscoverPeers      = "discover-peers"
	TaskTypeConfigPatch        = "config-patch"
	TaskTypeConfigApply        = "config-apply"
	TaskTypeConfigValidate     = "config-validate"
	TaskTypeConfigReload       = "config-reload"
	TaskTypeMarkReady          = "mark-ready"
	TaskTypeConfigureGenesis   = "configure-genesis"
	TaskTypeConfigureStateSync = "configure-state-sync"
	TaskTypeSnapshotUpload     = "snapshot-upload"
)

// SnapshotRestoreTask downloads and extracts a snapshot archive from S3.
type SnapshotRestoreTask struct {
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
	return TaskRequest{Type: t.TaskType(), Params: &p}
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
type SnapshotUploadTask struct {
	Bucket string
	Prefix string
	Region string
	Cron   string
}

func (t SnapshotUploadTask) TaskType() string { return TaskTypeSnapshotUpload }

func (t SnapshotUploadTask) Validate() error {
	if t.Bucket == "" {
		return fmt.Errorf("snapshot-upload: missing required field Bucket")
	}
	if t.Region == "" {
		return fmt.Errorf("snapshot-upload: missing required field Region")
	}
	if t.Cron != "" {
		if err := validateMinCronInterval(t.Cron, 24*time.Hour); err != nil {
			return fmt.Errorf("snapshot-upload: %w", err)
		}
	}
	return nil
}

// validateMinCronInterval parses a standard 5-field cron expression and
// checks that the gap between the first two scheduled firings is at least min.
func validateMinCronInterval(expr string, min time.Duration) error {
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	first := sched.Next(t0)
	second := sched.Next(first)
	if gap := second.Sub(first); gap < min {
		return fmt.Errorf("cron %q fires every %v, minimum allowed interval is %v", expr, gap, min)
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
	if t.Cron != "" {
		req.Schedule = &Schedule{Cron: &t.Cron}
	}
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

// ConfigureGenesisTask downloads genesis.json from an S3 URI.
type ConfigureGenesisTask struct {
	URI    string
	Region string
}

func (t ConfigureGenesisTask) TaskType() string { return TaskTypeConfigureGenesis }

func (t ConfigureGenesisTask) Validate() error {
	if t.URI == "" {
		return fmt.Errorf("configure-genesis: missing required field URI")
	}
	if t.Region == "" {
		return fmt.Errorf("configure-genesis: missing required field Region")
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
	p := map[string]interface{}{
		"uri":    t.URI,
		"region": t.Region,
	}
	return TaskRequest{Type: t.TaskType(), Params: &p}
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
	return TaskRequest{Type: t.TaskType(), Params: &p}
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
	Files map[string]map[string]any
}

func (t ConfigPatchTask) TaskType() string { return TaskTypeConfigPatch }

func (t ConfigPatchTask) Validate() error {
	return nil
}

func (t ConfigPatchTask) ToTaskRequest() TaskRequest {
	files := make(map[string]interface{}, len(t.Files))
	for k, v := range t.Files {
		files[k] = v
	}
	p := map[string]interface{}{"files": files}
	return TaskRequest{Type: t.TaskType(), Params: &p}
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
	if len(p) == 0 {
		return TaskRequest{Type: t.TaskType()}
	}
	return TaskRequest{Type: t.TaskType(), Params: &p}
}

// MarkReadyTask signals that bootstrap is complete.
type MarkReadyTask struct{}

func (t MarkReadyTask) TaskType() string { return TaskTypeMarkReady }
func (t MarkReadyTask) Validate() error  { return nil }

func (t MarkReadyTask) ToTaskRequest() TaskRequest {
	return TaskRequest{Type: t.TaskType()}
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
	return TaskRequest{Type: t.TaskType(), Params: &p}
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
	return TaskRequest{Type: t.TaskType()}
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
	return TaskRequest{Type: t.TaskType(), Params: &p}
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
