package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestAcquireInstanceLockRejectsSecondLiveInstance is the core regression
// test for the double-payout risk AcquireInstanceLock exists to prevent: a
// second process must never be allowed to start against the same database
// while the first is still alive.
func TestAcquireInstanceLockRejectsSecondLiveInstance(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "payments-gateway.db")

	release, err := AcquireInstanceLock(dbPath)
	if err != nil {
		t.Fatalf("first acquire: unexpected error: %v", err)
	}
	defer release()

	if _, err := AcquireInstanceLock(dbPath); err == nil {
		t.Fatalf("second acquire: expected an error while the first instance's lock is held, got nil")
	}
}

// TestAcquireInstanceLockReclaimsStaleLock confirms a lock file left behind
// by a process that is no longer running (simulated here with a PID that
// cannot possibly be alive) is treated as stale and reclaimed, rather than
// permanently blocking every future restart.
func TestAcquireInstanceLockReclaimsStaleLock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "payments-gateway.db")
	lockPath := dbPath + instanceLockSuffix

	// A PID this large is not a real running process on any platform this
	// service targets.
	const deadPID = 1 << 30
	if err := os.WriteFile(lockPath, []byte(strconv.Itoa(deadPID)), 0o644); err != nil {
		t.Fatalf("seed stale lock file: %v", err)
	}

	release, err := AcquireInstanceLock(dbPath)
	if err != nil {
		t.Fatalf("expected the stale lock to be reclaimed, got error: %v", err)
	}
	defer release()

	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read reclaimed lock file: %v", err)
	}
	if got, want := string(data), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("lock file pid = %q, want this process's pid %q", got, want)
	}
}

// TestAcquireInstanceLockReleaseAllowsReacquire confirms the release func
// returned by a successful acquire fully frees the lock, letting a
// subsequent (e.g. restarted) process acquire it again.
func TestAcquireInstanceLockReleaseAllowsReacquire(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "payments-gateway.db")

	release, err := AcquireInstanceLock(dbPath)
	if err != nil {
		t.Fatalf("first acquire: unexpected error: %v", err)
	}
	release()

	lockPath := dbPath + instanceLockSuffix
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected lock file to be removed after release, stat err = %v", err)
	}

	release2, err := AcquireInstanceLock(dbPath)
	if err != nil {
		t.Fatalf("second acquire after release: unexpected error: %v", err)
	}
	defer release2()
}

// TestPidAliveSelf confirms pidAlive correctly reports the current test
// process (guaranteed alive) as alive, and a PID that cannot exist as not
// alive, on whichever platform the test suite is running on.
func TestPidAliveSelf(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Fatalf("expected pidAlive(os.Getpid()) to be true for the running test process")
	}
	if pidAlive(1 << 30) {
		t.Fatalf("expected pidAlive to be false for an implausible pid")
	}
	if pidAlive(0) || pidAlive(-1) {
		t.Fatalf("expected pidAlive to be false for non-positive pids")
	}
}
