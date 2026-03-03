package engine

// Resource identifies a file or virtual resource that tasks may access.
type Resource string

const (
	ResourcePeersJSON     Resource = "peers.json"
	ResourceStateSyncJSON Resource = "statesync.json"
	ResourceConfigTOML    Resource = "config.toml"
	ResourceAppTOML       Resource = "app.toml"
	ResourceGenesisJSON   Resource = "genesis.json"
	ResourceData          Resource = "data/"
	ResourceReady         Resource = "engine:ready"
	ResourceSeidProcess   Resource = "seid-process"
	ResourceSnapshots     Resource = "snapshots/"
	ResourceUploadState   Resource = "upload-state.json"
)

// AccessMode distinguishes between shared (read) and exclusive (write) access.
type AccessMode int

const (
	AccessRead AccessMode = iota
	AccessWrite
)

// ResourceAccess pairs a resource with an access mode.
type ResourceAccess struct {
	Resource Resource
	Mode     AccessMode
}

// TaskResources maps each task type to its resource access requirements.
var TaskResources = map[TaskType][]ResourceAccess{
	TaskDiscoverPeers: {
		{ResourcePeersJSON, AccessWrite},
	},
	TaskConfigureStateSync: {
		{ResourcePeersJSON, AccessRead},
		{ResourceStateSyncJSON, AccessWrite},
	},
	TaskConfigPatch: {
		{ResourcePeersJSON, AccessRead},
		{ResourceStateSyncJSON, AccessRead},
		{ResourceConfigTOML, AccessWrite},
		{ResourceAppTOML, AccessWrite},
	},
	TaskConfigureGenesis: {
		{ResourceGenesisJSON, AccessWrite},
	},
	TaskSnapshotRestore: {
		{ResourceData, AccessWrite},
	},
	TaskMarkReady: {
		{ResourceReady, AccessWrite},
	},
	TaskUpdatePeers: {
		{ResourceConfigTOML, AccessWrite},
		{ResourceSeidProcess, AccessWrite},
	},
	TaskSnapshotUpload: {
		{ResourceSnapshots, AccessRead},
		{ResourceUploadState, AccessWrite},
	},
}
