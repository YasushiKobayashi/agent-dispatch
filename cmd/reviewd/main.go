// Command reviewd watches GitHub for review requests and runs Claude Code
// locally against an isolated worktree.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/YasushiKobayashi/agent-dispatch/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.Execute(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "reviewd: %v\n", err)
		os.Exit(1)
	}
}
