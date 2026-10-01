package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/YasushiKobayashi/agent-dispatch/internal/config"
)

func newRulesCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "設定ファイルを確認する",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newRulesValidateCmd(g))
	return cmd
}

func newRulesValidateCmd(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "config.yaml と rules.yaml を検証して内容を表示する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, rules, err := g.load()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "config: %s\n", g.configPath)
			fmt.Fprintf(out, "rules:  %s\n\n", g.rulesPath)
			fmt.Fprintf(out, "work_dir       %s\n", cfg.Paths.WorkDir)
			fmt.Fprintf(out, "claude         %s (mode %s, timeout %s, max_concurrent %d)\n",
				cfg.Claude.Path, cfg.Claude.Mode, cfg.Claude.Timeout, cfg.Claude.MaxConcurrent)
			if cfg.Claude.Mode == config.ModeGhostty {
				fmt.Fprintf(out, "ghostty        %s (window %s, activate=%t, keep_open=%t)\n",
					cfg.Ghostty.AppPath, cfg.Ghostty.Window, cfg.Ghostty.Activate, cfg.Ghostty.ShouldKeepOpen())
			}
			fmt.Fprintf(out, "gh             %s (login %q)\n", cfg.GitHub.GhPath, cfg.GitHub.Login)
			fmt.Fprintf(out, "notify         enabled=%t\n", cfg.Notify.Enabled)
			fmt.Fprintf(out, "max_jobs/poll  %d\n\n", cfg.Poll.MaxJobsPerPoll)

			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "RULE\tENABLED\tON\tREPOS\tMODE\tTIMEOUT")
			for _, r := range rules.Rules {
				kinds := make([]string, 0, len(r.On))
				for _, k := range r.On {
					kinds = append(kinds, string(k))
				}
				repos := strings.Join(r.Match.Repos, ",")
				if repos == "" {
					repos = "(全て)"
				}
				timeout := r.Action.Timeout.String()
				if r.Action.Timeout <= 0 {
					timeout = "(config の既定)"
				}
				fmt.Fprintf(w, "%s\t%t\t%s\t%s\t%s\t%s\n",
					r.Name, r.IsEnabled(), strings.Join(kinds, ","), repos,
					r.ModeOr(cfg.Claude.Mode), timeout)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(out, "\n%d 件のルールを読み込みました（有効: %d）\n", len(rules.Rules), len(rules.Enabled()))
			return nil
		},
	}
}
