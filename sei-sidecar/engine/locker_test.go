package engine

import "testing"

func TestTryAcquireSuccess(t *testing.T) {
	rl := NewResourceLocker()
	accesses := []ResourceAccess{
		{ResourcePeersJSON, AccessWrite},
	}
	if !rl.TryAcquire(accesses) {
		t.Fatal("expected acquire to succeed on idle locker")
	}
	rl.Release(accesses)
}

func TestTryAcquireWriteWriteBlocked(t *testing.T) {
	rl := NewResourceLocker()
	a1 := []ResourceAccess{{ResourceConfigTOML, AccessWrite}}
	a2 := []ResourceAccess{{ResourceConfigTOML, AccessWrite}}

	if !rl.TryAcquire(a1) {
		t.Fatal("first write acquire should succeed")
	}
	if rl.TryAcquire(a2) {
		t.Fatal("second write acquire should be blocked")
	}
	rl.Release(a1)
}

func TestTryAcquireReadReadAllowed(t *testing.T) {
	rl := NewResourceLocker()
	a1 := []ResourceAccess{{ResourcePeersJSON, AccessRead}}
	a2 := []ResourceAccess{{ResourcePeersJSON, AccessRead}}

	if !rl.TryAcquire(a1) {
		t.Fatal("first read acquire should succeed")
	}
	if !rl.TryAcquire(a2) {
		t.Fatal("second read acquire should also succeed")
	}
	rl.Release(a1)
	rl.Release(a2)
}

func TestTryAcquireReadWriteBlocked(t *testing.T) {
	rl := NewResourceLocker()
	a1 := []ResourceAccess{{ResourcePeersJSON, AccessRead}}
	a2 := []ResourceAccess{{ResourcePeersJSON, AccessWrite}}

	if !rl.TryAcquire(a1) {
		t.Fatal("read acquire should succeed")
	}
	if rl.TryAcquire(a2) {
		t.Fatal("write acquire should be blocked by reader")
	}
	rl.Release(a1)
}

func TestTryAcquireWriteReadBlocked(t *testing.T) {
	rl := NewResourceLocker()
	a1 := []ResourceAccess{{ResourcePeersJSON, AccessWrite}}
	a2 := []ResourceAccess{{ResourcePeersJSON, AccessRead}}

	if !rl.TryAcquire(a1) {
		t.Fatal("write acquire should succeed")
	}
	if rl.TryAcquire(a2) {
		t.Fatal("read acquire should be blocked by writer")
	}
	rl.Release(a1)
}

func TestTryAcquireAllOrNothingRollback(t *testing.T) {
	rl := NewResourceLocker()

	// Hold a write lock on config.toml.
	held := []ResourceAccess{{ResourceConfigTOML, AccessWrite}}
	if !rl.TryAcquire(held) {
		t.Fatal("initial acquire should succeed")
	}

	// Try to acquire peers.json (free) + config.toml (held) — should fail atomically.
	attempt := []ResourceAccess{
		{ResourcePeersJSON, AccessWrite},
		{ResourceConfigTOML, AccessWrite},
	}
	if rl.TryAcquire(attempt) {
		t.Fatal("should fail due to config.toml conflict")
	}

	// peers.json should still be free (rollback).
	free := []ResourceAccess{{ResourcePeersJSON, AccessWrite}}
	if !rl.TryAcquire(free) {
		t.Fatal("peers.json should be free after rollback")
	}
	rl.Release(free)
	rl.Release(held)
}

func TestReleaseThenReacquire(t *testing.T) {
	rl := NewResourceLocker()
	accesses := []ResourceAccess{{ResourceData, AccessWrite}}

	if !rl.TryAcquire(accesses) {
		t.Fatal("first acquire should succeed")
	}
	rl.Release(accesses)

	if !rl.TryAcquire(accesses) {
		t.Fatal("reacquire after release should succeed")
	}
	rl.Release(accesses)
}

func TestTryAcquireEmptyAccesses(t *testing.T) {
	rl := NewResourceLocker()
	if !rl.TryAcquire(nil) {
		t.Fatal("empty accesses should succeed")
	}
}

func TestTryAcquireDisjointResources(t *testing.T) {
	rl := NewResourceLocker()
	a1 := []ResourceAccess{{ResourceGenesisJSON, AccessWrite}}
	a2 := []ResourceAccess{{ResourceData, AccessWrite}}

	if !rl.TryAcquire(a1) {
		t.Fatal("first acquire should succeed")
	}
	if !rl.TryAcquire(a2) {
		t.Fatal("disjoint resource acquire should succeed")
	}
	rl.Release(a1)
	rl.Release(a2)
}
