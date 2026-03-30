package engine

import (
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	s, err := NewMemoryStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStoreSaveAndGet(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Nanosecond)
	completed := now.Add(time.Second)

	r := &TaskResult{
		ID:          "aaaaaaaa-1111-2222-3333-444444444444",
		Type:        "config-patch",
		Status:      TaskStatusCompleted,
		Params:      map[string]any{"file": "config.toml", "nested": map[string]any{"key": "val"}},
		Error:       "",
		SubmittedAt: now,
		CompletedAt: &completed,
	}

	if err := s.Save(r); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := s.Get(r.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if got.ID != r.ID {
		t.Fatalf("ID = %q, want %q", got.ID, r.ID)
	}
	if got.Type != r.Type {
		t.Fatalf("Type = %q, want %q", got.Type, r.Type)
	}
	if got.Status != r.Status {
		t.Fatalf("Status = %q, want %q", got.Status, r.Status)
	}
	if got.Error != r.Error {
		t.Fatalf("Error = %q, want %q", got.Error, r.Error)
	}
	if got.CompletedAt == nil {
		t.Fatal("expected non-nil CompletedAt")
	}

	// Verify nested params survived JSON round-trip.
	nested, ok := got.Params["nested"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested map, got %T", got.Params["nested"])
	}
	if nested["key"] != "val" {
		t.Fatalf("nested.key = %q, want %q", nested["key"], "val")
	}
}

func TestStoreGetNotFound(t *testing.T) {
	s := newTestStore(t)

	got, err := s.Get("nonexistent-id")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestStoreSaveUpsert(t *testing.T) {
	s := newTestStore(t)
	now := time.Now().Truncate(time.Nanosecond)

	r := &TaskResult{
		ID:          "bbbbbbbb-1111-2222-3333-444444444444",
		Type:        "config-patch",
		Status:      TaskStatusRunning,
		SubmittedAt: now,
	}
	if err := s.Save(r); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// Update status.
	completed := now.Add(time.Second)
	r.Status = TaskStatusCompleted
	r.CompletedAt = &completed
	if err := s.Save(r); err != nil {
		t.Fatalf("second save: %v", err)
	}

	got, _ := s.Get(r.ID)
	if got.Status != TaskStatusCompleted {
		t.Fatalf("Status = %q after upsert, want %q", got.Status, TaskStatusCompleted)
	}
	if got.CompletedAt == nil {
		t.Fatal("expected CompletedAt after upsert")
	}
}

func TestStoreListOrdering(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 5; i++ {
		r := &TaskResult{
			ID:          "list-" + string(rune('a'+i)) + "0000000-0000-0000-0000-000000000000",
			Type:        "config-patch",
			Status:      TaskStatusCompleted,
			SubmittedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := s.Save(r); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	results, err := s.List(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}

	// Newest first.
	for i := 1; i < len(results); i++ {
		if results[i].SubmittedAt.After(results[i-1].SubmittedAt) {
			t.Fatalf("results not ordered newest-first at index %d", i)
		}
	}
}

func TestStoreListLimit(t *testing.T) {
	s := newTestStore(t)
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 20; i++ {
		r := &TaskResult{
			ID:          "limit-" + string(rune('a'+i)) + "000000-0000-0000-0000-000000000000",
			Type:        "config-patch",
			Status:      TaskStatusCompleted,
			SubmittedAt: base.Add(time.Duration(i) * time.Minute),
		}
		if err := s.Save(r); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	results, err := s.List(5)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
}

func TestStoreDelete(t *testing.T) {
	s := newTestStore(t)

	r := &TaskResult{
		ID:          "cccccccc-1111-2222-3333-444444444444",
		Type:        "config-patch",
		Status:      TaskStatusCompleted,
		SubmittedAt: time.Now(),
	}
	if err := s.Save(r); err != nil {
		t.Fatal(err)
	}

	deleted, err := s.Delete(r.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !deleted {
		t.Fatal("expected delete to return true")
	}

	got, _ := s.Get(r.ID)
	if got != nil {
		t.Fatal("expected nil after delete")
	}
}

func TestStoreDeleteNotFound(t *testing.T) {
	s := newTestStore(t)

	deleted, err := s.Delete("nonexistent-id")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted {
		t.Fatal("expected delete to return false for nonexistent ID")
	}
}

func TestStoreMigrateIdempotent(t *testing.T) {
	s := newTestStore(t)

	// Running migrate again on the same DB should be a no-op.
	if err := migrate(s.db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestStoreNullableFields(t *testing.T) {
	s := newTestStore(t)

	r := &TaskResult{
		ID:          "dddddddd-1111-2222-3333-444444444444",
		Type:        "snapshot-restore",
		Status:      TaskStatusRunning,
		SubmittedAt: time.Now(),
		// CompletedAt nil; Params nil.
	}
	if err := s.Save(r); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := s.Get(r.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CompletedAt != nil {
		t.Fatal("expected nil CompletedAt")
	}
}
