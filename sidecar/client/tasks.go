package client

import (
	"fmt"
	"net/url"
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
	return TaskRequest{Type: t.TaskType(), Params: &p}
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
// No parameters are required; configuration is derived from the peers file.
type ConfigureStateSyncTask struct{}

func (t ConfigureStateSyncTask) TaskType() string { return TaskTypeConfigureStateSync }
func (t ConfigureStateSyncTask) Validate() error  { return nil }

func (t ConfigureStateSyncTask) ToTaskRequest() TaskRequest {
	return TaskRequest{Type: t.TaskType()}
}

// MarkReadyTask signals that bootstrap is complete.
type MarkReadyTask struct{}

func (t MarkReadyTask) TaskType() string { return TaskTypeMarkReady }
func (t MarkReadyTask) Validate() error  { return nil }

func (t MarkReadyTask) ToTaskRequest() TaskRequest {
	return TaskRequest{Type: t.TaskType()}
}
