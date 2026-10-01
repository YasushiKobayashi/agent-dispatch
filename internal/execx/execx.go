// Package execx runs external commands (gh, git, claude) with error messages
// that carry the child's stderr, which is what makes failures readable in a
// launchd log where nobody is watching the terminal.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner executes an external command and returns its stdout. It exists so
// tests can drive callers without the real binaries installed.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// RunnerFunc adapts a function to Runner.
type RunnerFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// Run calls f.
func (f RunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

// Exec is the Runner backed by os/exec.
type Exec struct {
	// Dir, when set, is the working directory for every command.
	Dir string
	// Env, when set, replaces the child's environment entirely.
	Env []string
}

// Run executes the command and returns stdout. A non-zero exit is reported as
// an error carrying the trimmed stderr.
func (e Exec) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = e.Dir
	cmd.Env = e.Env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, wrap(name, args, stderr.String(), err)
	}
	return stdout.Bytes(), nil
}

func wrap(name string, args []string, stderr string, err error) error {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = err.Error()
	}
	// Long stderr from a failing git/gh call is noise in a log line; the full
	// output is still in the run's own log file.
	if len(msg) > 2000 {
		msg = msg[:2000] + "… (truncated)"
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("%s %s: exit %d: %s", name, strings.Join(args, " "), exitErr.ExitCode(), msg)
	}
	return fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
}
