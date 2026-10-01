// Package lockfile provides the single-instance guard around `reviewd poll`.
//
// launchd fires on a fixed interval regardless of whether the previous run has
// finished, and a Claude Code review can easily outlast the interval.
package lockfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLocked is returned when another process already holds the lock.
var ErrLocked = errors.New("lock is held by another process")

// Lock is an advisory exclusive lock on a file.
type Lock struct {
	file *os.File
}

// Acquire takes the lock at path, creating the file and its parent directory.
// It returns ErrLocked if another process holds it.
func Acquire(path string) (*Lock, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}

	// Leaving the holder's pid in the file makes a stuck lock diagnosable.
	if err := file.Truncate(0); err == nil {
		fmt.Fprintf(file, "%d\n", os.Getpid())
	}
	return &Lock{file: file}, nil
}

// Release drops the lock. It is safe to call on a nil Lock.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	// Closing the descriptor releases the flock; unlocking first keeps the
	// window between the two from mattering.
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	err := l.file.Close()
	l.file = nil
	return err
}
