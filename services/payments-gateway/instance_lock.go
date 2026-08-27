package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// instanceLockSuffix is appended to the configured SQLite database path to
// derive the lock file's own path, keeping the two colocated without a
// separate config knob.
const instanceLockSuffix = ".lock"

// AcquireInstanceLock guards against two payments-gateway OS processes
// running against the same SQLite database at once. This service is
// single-instance by design: RedeemWatcher (redeem_watcher.go) coordinates
// its own tick loop against the admin confirm/fail/retry-payout endpoints
// purely via an in-process sync.Mutex (RedeemWatcher.mu), which provides
// zero protection across process boundaries. If two instances were ever
// running concurrently -- most plausibly during an overlapping deploy, where
// a new process starts before the old one has fully exited -- each would
// maintain its own private SQLite state and could independently discover the
// same on-chain pending redemption, each call NOWPayments to pay it out, and
// each only attempt to attest the outcome on-chain afterward. NOWPayments
// itself has no idempotency key tying a payout to a specific NHB burn, so
// this would double-spend real custodial funds with nothing short of manual
// reconciliation able to catch it after the fact.
//
// The guard is a plain PID file next to the SQLite database (dbPath+".lock"):
// on startup, if a lock file already exists and the PID it names is still
// alive, this returns an error and main.go fails fast rather than starting a
// second instance. Otherwise (no lock file, or a stale one naming a dead
// PID) this process claims it atomically and proceeds.
//
// A stdlib-only PID-file-with-liveness-check is used here rather than
// syscall.Flock: flock semantics differ enough between Windows and Unix (and
// this repo is developed on Windows but deployed to Linux) that getting a
// single implementation right for both is more delicate than this
// low-frequency, startup-only check warrants, and an OS-level advisory lock
// held by a process that crashed without releasing it can behave
// differently across platforms too. A PID file's liveness check (see
// pidAlive) is simple, portable, and self-heals: a lock left behind by a
// crashed process is automatically reclaimed by the next process to start,
// exactly as intended -- never permanently wedging future restarts.
//
// The returned release func removes the lock file; callers should defer it.
// Skipping the call on process exit is harmless: a lock file naming a dead
// PID is recognized as stale and reclaimed by the next startup either way.
func AcquireInstanceLock(dbPath string) (release func(), err error) {
	trimmed := strings.TrimSpace(dbPath)
	if trimmed == "" {
		return nil, fmt.Errorf("instance lock: database path is empty")
	}
	lockPath := trimmed + instanceLockSuffix
	pid := os.Getpid()
	pidBytes := []byte(strconv.Itoa(pid))

	// Two attempts: the first tries a straight atomic create (the expected
	// path on a clean startup). If that loses to an existing lock file, the
	// second attempt only runs after confirming the existing lock is stale
	// (names a dead PID) and removing it -- so a live second instance can
	// never win this loop, only ever a genuinely abandoned lock.
	for attempt := 0; attempt < 2; attempt++ {
		f, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if openErr == nil {
			if _, writeErr := f.Write(pidBytes); writeErr != nil {
				f.Close()
				os.Remove(lockPath)
				return nil, fmt.Errorf("instance lock: write %s: %w", lockPath, writeErr)
			}
			if closeErr := f.Close(); closeErr != nil {
				os.Remove(lockPath)
				return nil, fmt.Errorf("instance lock: close %s: %w", lockPath, closeErr)
			}
			return func() { releaseInstanceLock(lockPath, pid) }, nil
		}
		if !os.IsExist(openErr) {
			return nil, fmt.Errorf("instance lock: create %s: %w", lockPath, openErr)
		}

		data, readErr := os.ReadFile(lockPath)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				// Removed between the failed create and this read (another
				// process's own release racing us) -- just retry.
				continue
			}
			return nil, fmt.Errorf("instance lock: read existing %s: %w", lockPath, readErr)
		}
		if existingPID, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && pidAlive(existingPID) {
			return nil, fmt.Errorf("instance lock: another payments-gateway process (pid %d) already holds %s -- refusing to start a second instance against the same database (%s); running two at once risks each independently paying out the same on-chain redemption via NOWPayments before either attests it (see AcquireInstanceLock's doc comment). If you are certain no other instance is running, remove the lock file manually", existingPID, lockPath, trimmed)
		}
		// Stale: names a dead PID, or the file's content is unparseable
		// (e.g. a previous write was interrupted mid-way). Safe to reclaim.
		if removeErr := os.Remove(lockPath); removeErr != nil && !os.IsNotExist(removeErr) {
			return nil, fmt.Errorf("instance lock: remove stale lock %s: %w", lockPath, removeErr)
		}
	}
	return nil, fmt.Errorf("instance lock: could not acquire %s after reclaiming a stale lock", lockPath)
}

// releaseInstanceLock removes the lock file only if it still names this
// process's own PID -- guarding against ever clobbering a different
// instance's lock, which should be impossible under correct operation but
// costs nothing to check.
func releaseInstanceLock(lockPath string, pid int) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return
	}
	if strings.TrimSpace(string(data)) == strconv.Itoa(pid) {
		_ = os.Remove(lockPath)
	}
}

// pidAlive reports whether pid names a currently-running process, using only
// the standard library so behavior is consistent across this service's
// Windows development environment and Linux deployment target.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		// On Windows, os.FindProcess itself opens a handle via OpenProcess
		// and fails right here for a PID that no longer exists -- this is
		// the real liveness check on that platform. On Unix, FindProcess
		// always succeeds regardless of whether the process exists, so this
		// branch is unreachable there.
		return false
	}
	switch err := process.Signal(syscall.Signal(0)); {
	case err == nil:
		// Unix: signal 0 delivers nothing but confirms the process exists
		// and is signalable by us. Windows: FindProcess above already
		// proved the process is running, so a nil Signal error (impossible
		// today, since Windows only implements Kill/Interrupt) would only
		// ever mean "alive" too.
		return true
	case errors.Is(err, os.ErrProcessDone):
		return false
	default:
		// Any other error -- Unix EPERM (process exists but isn't ours to
		// signal, i.e. still alive) or Windows' "not supported" for a
		// non-Kill signal (FindProcess already proved this PID is alive) --
		// is treated as "still alive". This only ever widens the window
		// where a genuinely dead PID's stale lock requires the operator's
		// manual override above; it can never let two live instances run
		// at once.
		return true
	}
}
