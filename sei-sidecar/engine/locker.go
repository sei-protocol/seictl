package engine

import (
	"sort"
	"sync"
)

// ResourceLocker manages per-resource RWMutexes for concurrent task execution.
// TryAcquire is non-blocking and all-or-nothing: either all requested locks
// are acquired, or none are (with rollback).
type ResourceLocker struct {
	mu    sync.Mutex
	locks map[Resource]*lockState
}

type lockState struct {
	readers int
	writer  bool
}

// NewResourceLocker pre-creates lock state for all known resources.
func NewResourceLocker() *ResourceLocker {
	locks := make(map[Resource]*lockState)
	for _, accesses := range TaskResources {
		for _, a := range accesses {
			if _, ok := locks[a.Resource]; !ok {
				locks[a.Resource] = &lockState{}
			}
		}
	}
	return &ResourceLocker{locks: locks}
}

// TryAcquire attempts to acquire all requested resource locks atomically.
// Returns true if all locks were acquired; false if any conflict was detected
// (in which case no locks are held). Resources are acquired in sorted order
// to prevent deadlocks.
func (rl *ResourceLocker) TryAcquire(accesses []ResourceAccess) bool {
	if len(accesses) == 0 {
		return true
	}

	sorted := sortedAccesses(accesses)

	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Check all locks first (all-or-nothing).
	for _, a := range sorted {
		ls := rl.getOrCreate(a.Resource)
		if !canAcquire(ls, a.Mode) {
			return false
		}
	}

	// All checks passed — acquire.
	for _, a := range sorted {
		ls := rl.getOrCreate(a.Resource)
		acquire(ls, a.Mode)
	}
	return true
}

// Release releases all the given resource locks.
func (rl *ResourceLocker) Release(accesses []ResourceAccess) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	for _, a := range accesses {
		ls, ok := rl.locks[a.Resource]
		if !ok {
			continue
		}
		release(ls, a.Mode)
	}
}

func (rl *ResourceLocker) getOrCreate(r Resource) *lockState {
	ls, ok := rl.locks[r]
	if !ok {
		ls = &lockState{}
		rl.locks[r] = ls
	}
	return ls
}

func canAcquire(ls *lockState, mode AccessMode) bool {
	switch mode {
	case AccessRead:
		return !ls.writer
	case AccessWrite:
		return !ls.writer && ls.readers == 0
	}
	return false
}

func acquire(ls *lockState, mode AccessMode) {
	switch mode {
	case AccessRead:
		ls.readers++
	case AccessWrite:
		ls.writer = true
	}
}

func release(ls *lockState, mode AccessMode) {
	switch mode {
	case AccessRead:
		if ls.readers > 0 {
			ls.readers--
		}
	case AccessWrite:
		ls.writer = false
	}
}

func sortedAccesses(accesses []ResourceAccess) []ResourceAccess {
	sorted := make([]ResourceAccess, len(accesses))
	copy(sorted, accesses)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Resource < sorted[j].Resource
	})
	return sorted
}
