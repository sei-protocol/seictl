package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// noopHandler is a result-less handler; the engine's completion hook is what
// flips the readiness flag for mark-ready / mark-not-ready.
func noopHandler(context.Context, map[string]any) (json.RawMessage, error) { return nil, nil }

func engineOver(t *testing.T, store ResultStore, handlers map[TaskType]TaskHandler) *Engine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return NewEngine(ctx, handlers, store)
}

// seedStrandedMarkReady writes a mark-ready record stuck in "running", the
// state an ungraceful kill leaves between Submit's Save(running) and the
// completing Save. RehydrateStaleTasks re-runs exactly these.
func seedStrandedMarkReady(t *testing.T, store ResultStore) string {
	t.Helper()
	id := uuid.New().String()
	if err := store.Save(&TaskResult{
		ID:          id,
		Type:        string(TaskMarkReady),
		Status:      TaskStatusRunning,
		Run:         1,
		SubmittedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seeding stranded mark-ready: %v", err)
	}
	return id
}

// seedStrandedMarkNotReady writes a mark-not-ready record stuck in "running" —
// the state a crash leaves in the [Submit-saved-running … purge-commit] window.
func seedStrandedMarkNotReady(t *testing.T, store ResultStore) string {
	t.Helper()
	id := uuid.New().String()
	if err := store.Save(&TaskResult{
		ID:          id,
		Type:        string(TaskMarkNotReady),
		Status:      TaskStatusRunning,
		Run:         1,
		SubmittedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seeding stranded mark-not-ready: %v", err)
	}
	return id
}

func countMarkReady(t *testing.T, store ResultStore) int {
	t.Helper()
	results, err := store.List(100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	n := 0
	for _, r := range results {
		if r.Type == string(TaskMarkReady) {
			n++
		}
	}
	return n
}

func ensureStaysNotReady(t *testing.T, eng *Engine) {
	t.Helper()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if eng.Healthz() {
			t.Fatal("engine became ready after rehydration: a stranded mark-ready was not purged")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Control: demonstrates the release path the purge exists to close. A stranded
// running mark-ready rehydrated on restart re-marks the engine ready — which,
// mid-hold, would release seid onto a wiped data directory.
func TestRehydrate_StrandedMarkReadyReleasesWithoutPurge(t *testing.T) {
	store, dbPath := newFileStore(t)
	seedStrandedMarkReady(t, store)
	store = reopenStore(t, store, dbPath) // simulate a process restart

	eng := engineOver(t, store, map[TaskType]TaskHandler{TaskMarkReady: noopHandler})
	eng.RehydrateStaleTasks()

	waitForHealthz(t, eng)
}

// Guard: with the mark-ready records purged (what mark-not-ready's handler
// does) first, the restart's rehydration finds nothing to run and the engine
// stays not-ready across the restart.
func TestMarkNotReadyPurge_PreventsRehydrateRelease(t *testing.T) {
	store, dbPath := newFileStore(t)
	seedStrandedMarkReady(t, store)

	n, err := store.DeleteByType(string(TaskMarkReady))
	if err != nil || n != 1 {
		t.Fatalf("purge: n=%d err=%v (want 1, nil)", n, err)
	}

	store = reopenStore(t, store, dbPath) // simulate a process restart
	eng := engineOver(t, store, map[TaskType]TaskHandler{TaskMarkReady: noopHandler})
	eng.RehydrateStaleTasks()

	ensureStaysNotReady(t, eng)
}

// End-to-end through Submit: mark-ready makes the engine ready; mark-not-ready
// purges every mark-ready record and then the engine flips ready false. The
// purge (handler) completes before the flip (completion hook), so observing
// both post-conditions after the result lands proves purge-then-flip ordering.
func TestMarkNotReady_PurgesThenFlipsReadyFalse(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	purge := func(context.Context, map[string]any) (json.RawMessage, error) {
		if _, err := store.DeleteByType(string(TaskMarkReady)); err != nil {
			return nil, err
		}
		return nil, nil
	}
	eng := engineOver(t, store, map[TaskType]TaskHandler{
		TaskMarkReady:    noopHandler,
		TaskMarkNotReady: purge,
	})

	id, err := eng.Submit(Task{Type: TaskMarkReady})
	if err != nil {
		t.Fatalf("submit mark-ready: %v", err)
	}
	waitForResult(t, eng, id)
	waitForHealthz(t, eng)

	// A second stranded mark-ready alongside the completed one, so the purge
	// has to clear more than a single record.
	seedStrandedMarkReady(t, store)
	if countMarkReady(t, store) == 0 {
		t.Fatal("expected mark-ready records present before purge")
	}

	id2, err := eng.Submit(Task{Type: TaskMarkNotReady})
	if err != nil {
		t.Fatalf("submit mark-not-ready: %v", err)
	}
	waitForResult(t, eng, id2)

	if eng.Healthz() {
		t.Fatal("expected readiness flipped false after mark-not-ready")
	}
	if n := countMarkReady(t, store); n != 0 {
		t.Fatalf("expected all mark-ready records purged, got %d", n)
	}
}

// Both a mark-ready and a mark-not-ready stranded running by a crash in the
// [Submit-saved-running … purge-commit] window. RehydrateStaleTasks must run
// the hold synchronously first, so the flag ends deterministically false (held)
// with no dependence on goroutine scheduling. Looped to stress the ordering
// under -race, where the pre-fix concurrent dispatch would flake.
func TestRehydrate_StrandedHoldWinsDeterministically(t *testing.T) {
	for i := 0; i < 50; i++ {
		store, dbPath := newFileStore(t)
		seedStrandedMarkReady(t, store)
		seedStrandedMarkNotReady(t, store)
		store = reopenStore(t, store, dbPath) // simulate a process restart

		purge := func(context.Context, map[string]any) (json.RawMessage, error) {
			_, err := store.DeleteByType(string(TaskMarkReady))
			return nil, err
		}
		eng := engineOver(t, store, map[TaskType]TaskHandler{
			TaskMarkReady:    noopHandler,
			TaskMarkNotReady: purge,
		})
		eng.RehydrateStaleTasks()

		// Synchronous hold-first: the mark-not-ready purged the stranded
		// mark-ready before the re-list, so nothing dispatched can flip ready.
		if eng.Healthz() {
			t.Fatalf("iteration %d: engine ready after rehydration — hold released", i)
		}
	}
}

// On the live path the engine does NOT serialize a controller-submitted
// mark-ready against an active hold; that mutual exclusion is the controller's
// (reapproval suppression via adoptedWorkflow). This test asserts only that the
// engine survives the concurrency with no torn store state — the final readiness
// value is deliberately unasserted, being the controller's contract.
func TestLivePath_ConcurrentMarkReadyDuringHold_NoTornState(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	purge := func(context.Context, map[string]any) (json.RawMessage, error) {
		_, e := store.DeleteByType(string(TaskMarkReady))
		return nil, e
	}
	eng := engineOver(t, store, map[TaskType]TaskHandler{
		TaskMarkReady:    noopHandler,
		TaskMarkNotReady: purge,
	})

	// Become ready first.
	id0, err := eng.Submit(Task{Type: TaskMarkReady})
	if err != nil {
		t.Fatalf("submit initial mark-ready: %v", err)
	}
	waitForResult(t, eng, id0)
	waitForHealthz(t, eng)

	// Race a hold against a fresh mark-ready.
	holdID := uuid.New().String()
	readyID := uuid.New().String()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = eng.Submit(Task{ID: holdID, Type: TaskMarkNotReady}) }()
	go func() { defer wg.Done(); _, _ = eng.Submit(Task{ID: readyID, Type: TaskMarkReady}) }()
	wg.Wait()

	// The hold's own record is never self-purged, so it reaches terminal.
	waitForResult(t, eng, holdID)

	// Drain: wait until no row is left running (the concurrent mark-ready either
	// completed or was purged mid-flight — both terminal for our purposes), then
	// a brief settle for any in-flight completed-save to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		results, err := store.List(100)
		if err != nil {
			t.Fatalf("list during drain: %v", err)
		}
		if countRunning(results) == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)

	// No torn state: the store is queryable and no row is stuck running.
	results, err := store.List(100)
	if err != nil {
		t.Fatalf("list after race: %v", err)
	}
	if n := countRunning(results); n != 0 {
		t.Errorf("expected no running rows after concurrent hold/ready, got %d", n)
	}

	// Final readiness is the controller's contract, not the engine's — do not
	// assert it. Reading it just confirms the atomic is well-defined either way.
	_ = eng.Healthz()
}

func countRunning(results []TaskResult) int {
	n := 0
	for _, r := range results {
		if r.Status == TaskStatusRunning {
			n++
		}
	}
	return n
}

// failingPurgeStore is a ResultStore whose DeleteByType always fails, modelling
// a broken store during a hold's purge. Every other operation delegates.
type failingPurgeStore struct {
	*SQLiteStore
}

func (failingPurgeStore) DeleteByType(string) (int, error) {
	return 0, fmt.Errorf("simulated purge failure")
}

// When the synchronous hold's purge fails, the stranded mark-ready survives —
// the holdSeen fail-closed guard must still skip dispatching it, leaving it
// running and readiness false (never release a hold on a broken store).
func TestRehydrate_FailedPurgeLeavesStrandedMarkReadyHeld(t *testing.T) {
	inner, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	store := failingPurgeStore{inner}

	readyID := seedStrandedMarkReady(t, store)
	seedStrandedMarkNotReady(t, store)

	purge := func(context.Context, map[string]any) (json.RawMessage, error) {
		_, err := store.DeleteByType(string(TaskMarkReady))
		return nil, err // fails
	}
	eng := engineOver(t, store, map[TaskType]TaskHandler{
		TaskMarkReady:    noopHandler,
		TaskMarkNotReady: purge,
	})
	eng.RehydrateStaleTasks()

	if eng.Healthz() {
		t.Fatal("engine ready after rehydration: stranded mark-ready was dispatched despite the hold guard")
	}
	r, err := store.Get(readyID)
	if err != nil {
		t.Fatalf("get stranded mark-ready: %v", err)
	}
	if r == nil {
		t.Fatal("stranded mark-ready was removed; a failed purge should leave it running")
	}
	if r.Status != TaskStatusRunning {
		t.Errorf("stranded mark-ready status = %q, want running (skipped, not dispatched)", r.Status)
	}
	ensureStaysNotReady(t, eng)
}

// relistFailStore fails the second (post-purge) ListStaleTasks call, modelling a
// store that breaks between the hold's purge and the dispatch loop.
type relistFailStore struct {
	*SQLiteStore
	listCalls int
}

func (s *relistFailStore) ListStaleTasks() ([]TaskResult, error) {
	s.listCalls++
	if s.listCalls >= 2 {
		return nil, fmt.Errorf("simulated re-list failure")
	}
	return s.SQLiteStore.ListStaleTasks()
}

// A re-list error after the synchronous hold must abort rehydration before the
// dispatch loop: no stale task is dispatched and readiness stays false.
func TestRehydrate_RelistErrorAbortsDispatch(t *testing.T) {
	inner, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("memory store: %v", err)
	}
	t.Cleanup(func() { _ = inner.Close() })
	store := &relistFailStore{SQLiteStore: inner}

	seedStrandedMarkNotReady(t, store)
	// A third stale task that loop 2 would dispatch if it were reached.
	otherID := uuid.New().String()
	if err := store.Save(&TaskResult{
		ID:          otherID,
		Type:        string(TaskConfigPatch),
		Status:      TaskStatusRunning,
		Run:         1,
		SubmittedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seeding other stale task: %v", err)
	}

	var otherRan atomic.Bool
	handlers := map[TaskType]TaskHandler{
		TaskMarkNotReady: func(context.Context, map[string]any) (json.RawMessage, error) { return nil, nil },
		TaskConfigPatch: func(context.Context, map[string]any) (json.RawMessage, error) {
			otherRan.Store(true)
			return nil, nil
		},
	}
	eng := engineOver(t, store, handlers)
	eng.RehydrateStaleTasks()

	ensureStaysNotReady(t, eng)
	time.Sleep(50 * time.Millisecond) // give any erroneous dispatch a chance to run
	if otherRan.Load() {
		t.Fatal("a stale task was dispatched despite the re-list error")
	}
}
