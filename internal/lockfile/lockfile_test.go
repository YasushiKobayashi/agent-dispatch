package lockfile

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAcquireAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "reviewd.lock")

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if pid, cerr := strconv.Atoi(strings.TrimSpace(string(body))); cerr != nil || pid != os.Getpid() {
		t.Errorf("lock file contains %q, want this process's pid %d", body, os.Getpid())
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Releasing twice must not panic or error.
	if err := lock.Release(); err != nil {
		t.Errorf("second Release: %v", err)
	}

	again, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	again.Release()
}

// flock is tied to the open file description, so a second Acquire conflicts
// even within one process.
func TestSecondAcquireInTheSameProcessIsBlocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reviewd.lock")

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock.Release()

	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Acquire = %v, want ErrLocked", err)
	}
}

// The contention that matters in production is between processes, which is
// what launchd creates when a poll outlasts its interval.
func TestAcquireFromAnotherProcessIsBlocked(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	path := filepath.Join(t.TempDir(), "reviewd.lock")

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lock.Release()

	helper := exec.Command(os.Args[0], "-test.run=TestHelperAcquire")
	helper.Env = append(os.Environ(), "REVIEWD_LOCK_PATH="+path)
	out, err := helper.CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "LOCKED") {
		t.Errorf("helper output = %q, want it to report LOCKED", out)
	}
}

// TestHelperAcquire runs in the subprocess spawned above.
func TestHelperAcquire(t *testing.T) {
	path := os.Getenv("REVIEWD_LOCK_PATH")
	if path == "" {
		t.Skip("not running as the lock helper")
	}
	_, err := Acquire(path)
	if errors.Is(err, ErrLocked) {
		// Printed rather than logged so the parent sees it without -v.
		fmt.Println("LOCKED")
		return
	}
	t.Fatalf("Acquire in a second process = %v, want ErrLocked", err)
}

func TestAcquireReportsUnusablePath(t *testing.T) {
	// A path whose parent is a regular file cannot be created.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Acquire(filepath.Join(blocker, "reviewd.lock")); err == nil {
		t.Fatal("Acquire succeeded on an unusable path")
	}
}
