package engine

import "testing"

func TestAllTaskTypesHaveResourceDeclarations(t *testing.T) {
	expectedTasks := []TaskType{
		TaskDiscoverPeers,
		TaskConfigureStateSync,
		TaskConfigPatch,
		TaskConfigureGenesis,
		TaskSnapshotRestore,
		TaskMarkReady,
		TaskUpdatePeers,
		TaskSnapshotUpload,
	}

	for _, tt := range expectedTasks {
		if _, ok := TaskResources[tt]; !ok {
			t.Errorf("task type %q missing resource declaration", tt)
		}
	}
}

func TestResourceConflictMatrix(t *testing.T) {
	tests := []struct {
		name     string
		taskA    TaskType
		taskB    TaskType
		conflict bool
	}{
		{
			name:     "config-patch vs update-peers conflict on config.toml",
			taskA:    TaskConfigPatch,
			taskB:    TaskUpdatePeers,
			conflict: true,
		},
		{
			name:     "genesis vs snapshot-upload no conflict",
			taskA:    TaskConfigureGenesis,
			taskB:    TaskSnapshotUpload,
			conflict: false,
		},
		{
			name:     "discover-peers vs config-patch conflict on peers.json",
			taskA:    TaskDiscoverPeers,
			taskB:    TaskConfigPatch,
			conflict: true,
		},
		{
			name:     "discover-peers vs configure-state-sync conflict on peers.json",
			taskA:    TaskDiscoverPeers,
			taskB:    TaskConfigureStateSync,
			conflict: true,
		},
		{
			name:     "snapshot-restore vs genesis no conflict",
			taskA:    TaskSnapshotRestore,
			taskB:    TaskConfigureGenesis,
			conflict: false,
		},
		{
			name:     "configure-state-sync vs config-patch read-read peers.json allowed",
			taskA:    TaskConfigureStateSync,
			taskB:    TaskConfigPatch,
			conflict: true, // statesync writes statesync.json, config-patch reads it
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rl := NewResourceLocker()
			if !rl.TryAcquire(TaskResources[tt.taskA]) {
				t.Fatal("first acquire should always succeed")
			}
			got := !rl.TryAcquire(TaskResources[tt.taskB])
			if got != tt.conflict {
				t.Fatalf("expected conflict=%v, got %v", tt.conflict, got)
			}
			rl.Release(TaskResources[tt.taskA])
		})
	}
}
