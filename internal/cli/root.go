// Package cli defines reviewd's command line surface.
package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/YasushiKobayashi/agent-dispatch/internal/app"
	"github.com/YasushiKobayashi/agent-dispatch/internal/config"
)

// Version is stamped at build time via -ldflags.
var Version = "dev"

type globals struct {
	configPath string
	rulesPath  string
	verbose    bool
}

// Execute runs the root command.
func Execute(ctx context.Context) error {
	g := &globals{}

	root := &cobra.Command{
		Use:   "reviewd",
		Short: "レビュー依頼を検知してローカルの Claude Code を自動起動する",
		Long: strings.TrimSpace(`
reviewd は Mac に常駐し、GitHub のレビュー依頼を検知して、
作業中のブランチから隔離された worktree でローカルの Claude Code を起動します。

  reviewd daemon install   launchd に登録して常駐させる
  reviewd seed             既存のレビュー依頼を「処理済み」にして初回の一斉発火を防ぐ
  reviewd poll --dry-run   何が拾われるかだけ確認する
`),
		Version:           Version,
		SilenceUsage:      true,
		SilenceErrors:     true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error { return g.resolveDefaults() },
	}

	root.PersistentFlags().StringVar(&g.configPath, "config", "", "config.yaml のパス (既定: <config dir>/reviewd/config.yaml)")
	root.PersistentFlags().StringVar(&g.rulesPath, "rules", "", "rules.yaml のパス (既定: <config dir>/reviewd/rules.yaml)")
	root.PersistentFlags().BoolVarP(&g.verbose, "verbose", "v", false, "デバッグログを出す")

	root.AddCommand(
		newPollCmd(g),
		newSeedCmd(g),
		newRunCmd(g),
		newStatusCmd(g),
		newRulesCmd(g),
		newInitCmd(g),
		newDaemonCmd(g),
	)
	return root.ExecuteContext(ctx)
}

// resolveDefaults fills in the config and rules paths from the user's config
// directory when the flags were not given.
func (g *globals) resolveDefaults() error {
	dir, err := config.DefaultDir()
	if err != nil {
		return err
	}
	if g.configPath == "" {
		g.configPath = filepath.Join(dir, "config.yaml")
	}
	if g.rulesPath == "" {
		g.rulesPath = filepath.Join(dir, "rules.yaml")
	}

	expanded, err := config.ExpandPath(g.configPath)
	if err != nil {
		return err
	}
	g.configPath = expanded

	expanded, err = config.ExpandPath(g.rulesPath)
	if err != nil {
		return err
	}
	g.rulesPath = expanded
	return nil
}

func (g *globals) logger() *slog.Logger {
	level := slog.LevelInfo
	if g.verbose {
		level = slog.LevelDebug
	}
	// stderr, so `reviewd status` output stays pipeable and launchd's
	// StandardErrorPath captures the diagnostics.
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func (g *globals) load() (config.Config, config.Rules, error) {
	cfg, err := config.Load(g.configPath)
	if err != nil {
		return config.Config{}, config.Rules{}, err
	}
	rules, err := config.LoadRules(g.rulesPath)
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
			return config.Config{}, config.Rules{}, fmt.Errorf(
				"%w\n\nrules.yaml がありません。`reviewd init` で雛形を作れます", err)
		}
		return config.Config{}, config.Rules{}, err
	}
	return cfg, rules, nil
}

// open loads the configuration and wires an App.
func (g *globals) open() (*app.App, config.Config, error) {
	cfg, rules, err := g.load()
	if err != nil {
		return nil, config.Config{}, err
	}
	a, err := app.Open(cfg, rules, g.logger(), app.Deps{})
	if err != nil {
		return nil, config.Config{}, err
	}
	return a, cfg, nil
}
