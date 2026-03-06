package client

import (
	"encoding/json"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

func genNonEmptyString() gopter.Gen {
	return gen.AlphaString().SuchThat(func(v string) bool { return len(v) > 0 })
}

func genSnapshotRestoreTask() gopter.Gen {
	return gopter.CombineGens(
		genNonEmptyString(),
		genNonEmptyString(),
		genNonEmptyString(),
		genNonEmptyString(),
	).Map(func(v []interface{}) SnapshotRestoreTask {
		return SnapshotRestoreTask{
			Bucket:  v[0].(string),
			Prefix:  v[1].(string),
			Region:  v[2].(string),
			ChainID: v[3].(string),
		}
	})
}

func genSnapshotUploadTask() gopter.Gen {
	return gopter.CombineGens(
		genNonEmptyString(),
		genNonEmptyString(),
		genNonEmptyString(),
	).Map(func(v []interface{}) SnapshotUploadTask {
		return SnapshotUploadTask{
			Bucket: v[0].(string),
			Prefix: v[1].(string),
			Region: v[2].(string),
		}
	})
}

func genConfigureGenesisTask() gopter.Gen {
	return gopter.CombineGens(
		genNonEmptyString(),
		genNonEmptyString(),
		genNonEmptyString(),
	).Map(func(v []interface{}) ConfigureGenesisTask {
		return ConfigureGenesisTask{
			URI:    "s3://" + v[0].(string) + "/" + v[1].(string),
			Region: v[2].(string),
		}
	})
}

func genEC2TagsSource() gopter.Gen {
	return gopter.CombineGens(
		genNonEmptyString(),
		genNonEmptyString(),
		genNonEmptyString(),
	).Map(func(v []interface{}) PeerSource {
		return PeerSource{
			Type:   PeerSourceEC2Tags,
			Region: v[0].(string),
			Tags:   map[string]string{v[1].(string): v[2].(string)},
		}
	})
}

func genStaticSource() gopter.Gen {
	return genNonEmptyString().Map(func(v string) PeerSource {
		return PeerSource{
			Type:      PeerSourceStatic,
			Addresses: []string{v},
		}
	})
}

func genPeerSource() gopter.Gen {
	return gen.OneGenOf(genEC2TagsSource(), genStaticSource())
}

func genDiscoverPeersTask() gopter.Gen {
	return gen.SliceOfN(3, genPeerSource()).SuchThat(func(v []PeerSource) bool {
		return len(v) > 0
	}).Map(func(v []PeerSource) DiscoverPeersTask {
		return DiscoverPeersTask{Sources: v}
	})
}

func genConfigPatchTask() gopter.Gen {
	return gopter.CombineGens(
		genNonEmptyString(),
		genNonEmptyString(),
	).Map(func(v []interface{}) ConfigPatchTask {
		return ConfigPatchTask{
			Files: map[string]map[string]any{
				"config.toml": {
					"p2p": map[string]any{"persistent-peers": v[0].(string)},
				},
				"app.toml": {
					"pruning": v[1].(string),
				},
			},
		}
	})
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	properties := gopter.NewProperties(gopter.DefaultTestParameters())
	properties.Property("SnapshotRestoreTask round-trips through TaskRequest", prop.ForAll(
		func(task SnapshotRestoreTask) bool {
			if err := task.Validate(); err != nil {
				return false
			}
			req := task.ToTaskRequest()
			if req.Type != TaskTypeSnapshotRestore {
				return false
			}
			rebuilt := SnapshotRestoreTaskFromParams(*req.Params)
			return rebuilt.Bucket == task.Bucket &&
				rebuilt.Prefix == task.Prefix &&
				rebuilt.Region == task.Region &&
				rebuilt.ChainID == task.ChainID
		},
		genSnapshotRestoreTask(),
	))
	properties.TestingRun(t)
}

func TestSnapshotUploadRoundTrip(t *testing.T) {
	properties := gopter.NewProperties(gopter.DefaultTestParameters())
	properties.Property("SnapshotUploadTask round-trips through TaskRequest", prop.ForAll(
		func(task SnapshotUploadTask) bool {
			if err := task.Validate(); err != nil {
				return false
			}
			req := task.ToTaskRequest()
			if req.Type != TaskTypeSnapshotUpload {
				return false
			}
			rebuilt := SnapshotUploadTaskFromParams(*req.Params)
			return rebuilt.Bucket == task.Bucket &&
				rebuilt.Prefix == task.Prefix &&
				rebuilt.Region == task.Region
		},
		genSnapshotUploadTask(),
	))
	properties.TestingRun(t)
}

func TestConfigureGenesisRoundTrip(t *testing.T) {
	properties := gopter.NewProperties(gopter.DefaultTestParameters())
	properties.Property("ConfigureGenesisTask round-trips through TaskRequest", prop.ForAll(
		func(task ConfigureGenesisTask) bool {
			if err := task.Validate(); err != nil {
				return false
			}
			req := task.ToTaskRequest()
			if req.Type != TaskTypeConfigureGenesis {
				return false
			}
			rebuilt := ConfigureGenesisTaskFromParams(*req.Params)
			return rebuilt.URI == task.URI && rebuilt.Region == task.Region
		},
		genConfigureGenesisTask(),
	))
	properties.TestingRun(t)
}

func TestDiscoverPeersRoundTrip(t *testing.T) {
	properties := gopter.NewProperties(gopter.DefaultTestParameters())
	properties.Property("DiscoverPeersTask round-trips through TaskRequest", prop.ForAll(
		func(task DiscoverPeersTask) bool {
			if err := task.Validate(); err != nil {
				return false
			}
			req := task.ToTaskRequest()
			if req.Type != TaskTypeDiscoverPeers {
				return false
			}
			rebuilt, err := DiscoverPeersTaskFromParams(*req.Params)
			if err != nil {
				return false
			}
			if len(rebuilt.Sources) != len(task.Sources) {
				return false
			}
			for i, src := range task.Sources {
				r := rebuilt.Sources[i]
				if r.Type != src.Type {
					return false
				}
				switch src.Type {
				case PeerSourceEC2Tags:
					if r.Region != src.Region {
						return false
					}
					if len(r.Tags) != len(src.Tags) {
						return false
					}
					for k, v := range src.Tags {
						if r.Tags[k] != v {
							return false
						}
					}
				case PeerSourceStatic:
					if len(r.Addresses) != len(src.Addresses) {
						return false
					}
					for j, a := range src.Addresses {
						if r.Addresses[j] != a {
							return false
						}
					}
				}
			}
			return true
		},
		genDiscoverPeersTask(),
	))
	properties.TestingRun(t)
}

func TestConfigPatchRoundTrip(t *testing.T) {
	properties := gopter.NewProperties(gopter.DefaultTestParameters())
	properties.Property("ConfigPatchTask round-trips through TaskRequest", prop.ForAll(
		func(task ConfigPatchTask) bool {
			if err := task.Validate(); err != nil {
				return false
			}
			req := task.ToTaskRequest()
			if req.Type != TaskTypeConfigPatch {
				return false
			}
			if req.Params == nil {
				return false
			}
			files, ok := (*req.Params)["files"]
			if !ok {
				return false
			}
			filesMap, ok := files.(map[string]interface{})
			if !ok {
				return false
			}
			return len(filesMap) == len(task.Files)
		},
		genConfigPatchTask(),
	))
	properties.TestingRun(t)
}

func TestConfigureStateSyncRoundTrip(t *testing.T) {
	task := ConfigureStateSyncTask{}
	if err := task.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	req := task.ToTaskRequest()
	if req.Type != TaskTypeConfigureStateSync {
		t.Errorf("Type = %q, want %q", req.Type, TaskTypeConfigureStateSync)
	}
	if req.Params != nil {
		t.Errorf("Params = %v, want nil", req.Params)
	}
}

func TestMarkReadyRoundTrip(t *testing.T) {
	task := MarkReadyTask{}
	if err := task.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	req := task.ToTaskRequest()
	if req.Type != TaskTypeMarkReady {
		t.Errorf("Type = %q, want %q", req.Type, TaskTypeMarkReady)
	}
	if req.Params != nil {
		t.Errorf("Params = %v, want nil", req.Params)
	}
}

func TestSnapshotRestoreValidationRejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		task SnapshotRestoreTask
	}{
		{"missing bucket", SnapshotRestoreTask{Prefix: "p", Region: "r", ChainID: "c"}},
		{"missing prefix", SnapshotRestoreTask{Bucket: "b", Region: "r", ChainID: "c"}},
		{"missing region", SnapshotRestoreTask{Bucket: "b", Prefix: "p", ChainID: "c"}},
		{"missing chainId", SnapshotRestoreTask{Bucket: "b", Prefix: "p", Region: "r"}},
		{"all empty", SnapshotRestoreTask{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.task.Validate(); err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}

func TestSnapshotUploadValidationRejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		task SnapshotUploadTask
	}{
		{"missing bucket", SnapshotUploadTask{Region: "r"}},
		{"missing region", SnapshotUploadTask{Bucket: "b"}},
		{"all empty", SnapshotUploadTask{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.task.Validate(); err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}

func TestConfigureGenesisValidation(t *testing.T) {
	cases := []struct {
		name string
		task ConfigureGenesisTask
		ok   bool
	}{
		{"valid", ConfigureGenesisTask{URI: "s3://bucket/key", Region: "us-east-1"}, true},
		{"missing uri", ConfigureGenesisTask{Region: "us-east-1"}, false},
		{"missing region", ConfigureGenesisTask{URI: "s3://bucket/key"}, false},
		{"wrong scheme", ConfigureGenesisTask{URI: "https://bucket/key", Region: "us-east-1"}, false},
		{"no key", ConfigureGenesisTask{URI: "s3://bucket", Region: "us-east-1"}, false},
		{"no key trailing slash", ConfigureGenesisTask{URI: "s3://bucket/", Region: "us-east-1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.task.Validate()
			if tc.ok && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}

func TestDiscoverPeersValidation(t *testing.T) {
	cases := []struct {
		name string
		task DiscoverPeersTask
		ok   bool
	}{
		{"empty sources", DiscoverPeersTask{}, false},
		{"ec2Tags missing region", DiscoverPeersTask{Sources: []PeerSource{{Type: PeerSourceEC2Tags, Tags: map[string]string{"k": "v"}}}}, false},
		{"ec2Tags missing tags", DiscoverPeersTask{Sources: []PeerSource{{Type: PeerSourceEC2Tags, Region: "us-east-1"}}}, false},
		{"static missing addresses", DiscoverPeersTask{Sources: []PeerSource{{Type: PeerSourceStatic}}}, false},
		{"unknown type", DiscoverPeersTask{Sources: []PeerSource{{Type: "unknown"}}}, false},
		{"valid ec2Tags", DiscoverPeersTask{Sources: []PeerSource{{Type: PeerSourceEC2Tags, Region: "us-east-1", Tags: map[string]string{"k": "v"}}}}, true},
		{"valid static", DiscoverPeersTask{Sources: []PeerSource{{Type: PeerSourceStatic, Addresses: []string{"addr1"}}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.task.Validate()
			if tc.ok && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}

func TestConfigPatchToTaskRequest_NestedValuesPreserved(t *testing.T) {
	task := ConfigPatchTask{
		Files: map[string]map[string]any{
			"config.toml": {
				"statesync": map[string]any{
					"use-local-snapshot": true,
					"backfill-blocks":    int64(0),
				},
				"p2p": map[string]any{
					"persistent-peers": "abc@1.2.3.4:26656",
				},
			},
			"app.toml": {
				"pruning":           "nothing",
				"snapshot-interval": int64(2000),
			},
		},
	}

	req := task.ToTaskRequest()

	// Simulate the JSON round-trip that happens on the wire.
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded TaskRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	files, ok := (*decoded.Params)["files"].(map[string]any)
	if !ok {
		t.Fatal("expected files to be a map after JSON round-trip")
	}

	configToml, ok := files["config.toml"].(map[string]any)
	if !ok {
		t.Fatal("expected config.toml entry to be a map")
	}
	statesync, ok := configToml["statesync"].(map[string]any)
	if !ok {
		t.Fatal("expected statesync section to be a map")
	}
	if statesync["use-local-snapshot"] != true {
		t.Errorf("use-local-snapshot = %v, want true", statesync["use-local-snapshot"])
	}

	appToml, ok := files["app.toml"].(map[string]any)
	if !ok {
		t.Fatal("expected app.toml entry to be a map")
	}
	if appToml["pruning"] != "nothing" {
		t.Errorf("pruning = %v, want nothing", appToml["pruning"])
	}
	// JSON unmarshals numbers as float64.
	if appToml["snapshot-interval"] != float64(2000) {
		t.Errorf("snapshot-interval = %v, want 2000", appToml["snapshot-interval"])
	}
}

func TestConfigPatchValidationAcceptsEmpty(t *testing.T) {
	task := ConfigPatchTask{}
	if err := task.Validate(); err != nil {
		t.Errorf("expected nil error for empty ConfigPatchTask, got %v", err)
	}
}

func TestStatusResponseJSONRoundTrip(t *testing.T) {
	properties := gopter.NewProperties(gopter.DefaultTestParameters())
	properties.Property("StatusResponse JSON round-trips", prop.ForAll(
		func(status StatusResponseStatus) bool {
			sr := StatusResponse{Status: status}
			data, err := json.Marshal(sr)
			if err != nil {
				return false
			}
			var decoded StatusResponse
			if err := json.Unmarshal(data, &decoded); err != nil {
				return false
			}
			return decoded.Status == sr.Status
		},
		gen.OneConstOf(Initializing, Running, Ready),
	))
	properties.TestingRun(t)
}

func TestTaskRequestJSONRoundTrip(t *testing.T) {
	properties := gopter.NewProperties(gopter.DefaultTestParameters())
	properties.Property("TaskRequest JSON round-trips preserve type", prop.ForAll(
		func(taskType string) bool {
			req := TaskRequest{Type: taskType}
			data, err := json.Marshal(req)
			if err != nil {
				return false
			}
			var decoded TaskRequest
			if err := json.Unmarshal(data, &decoded); err != nil {
				return false
			}
			return decoded.Type == taskType
		},
		gen.OneConstOf(
			TaskTypeSnapshotRestore,
			TaskTypeDiscoverPeers,
			TaskTypeConfigPatch,
			TaskTypeMarkReady,
			TaskTypeConfigureGenesis,
			TaskTypeConfigureStateSync,
			TaskTypeSnapshotUpload,
		),
	))
	properties.TestingRun(t)
}

func TestScheduleJSONRoundTrip(t *testing.T) {
	cron := "*/5 * * * *"
	height := int64(12345)
	cases := []struct {
		name  string
		sched Schedule
	}{
		{"cron only", Schedule{Cron: &cron}},
		{"block height only", Schedule{BlockHeight: &height}},
		{"both", Schedule{Cron: &cron, BlockHeight: &height}},
		{"empty", Schedule{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.sched)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var decoded Schedule
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if (tc.sched.Cron == nil) != (decoded.Cron == nil) {
				t.Errorf("Cron nil mismatch")
			}
			if tc.sched.Cron != nil && *tc.sched.Cron != *decoded.Cron {
				t.Errorf("Cron = %q, want %q", *decoded.Cron, *tc.sched.Cron)
			}
			if (tc.sched.BlockHeight == nil) != (decoded.BlockHeight == nil) {
				t.Errorf("BlockHeight nil mismatch")
			}
			if tc.sched.BlockHeight != nil && *tc.sched.BlockHeight != *decoded.BlockHeight {
				t.Errorf("BlockHeight = %d, want %d", *decoded.BlockHeight, *tc.sched.BlockHeight)
			}
		})
	}
}
