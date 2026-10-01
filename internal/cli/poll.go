package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/YasushiKobayashi/agent-dispatch/internal/app"
)

func newPollCmd(g *globals) *cobra.Command {
	var (
		dryRun bool
		limit  int
	)

	cmd := &cobra.Command{
		Use:   "poll",
		Short: "1 回ポーリングして、条件に合うレビュー依頼で Claude Code を起動する",
		Long: strings.TrimSpace(`
launchd から定期的に呼ばれる本体。

--dry-run を付けると検知とルール判定だけ行い、実行も記録もしません。
拾われるはずの PR が拾われないときは、まずこれで理由を確認してください。
`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, _, err := g.open()
			if err != nil {
				return err
			}
			defer a.Close()

			report, err := a.Poll(cmd.Context(), app.PollOptions{DryRun: dryRun, Limit: limit})
			if err != nil {
				return err
			}
			printPollReport(cmd.OutOrStdout(), report, dryRun || g.verbose)
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "検知と判定だけ行い、実行も記録もしない")
	cmd.Flags().IntVar(&limit, "limit", 0, "検索で見る PR の上限 (既定: 50)")
	return cmd
}

func newSeedCmd(g *globals) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "seed",
		Short: "現在のレビュー依頼を実行せず「処理済み」として記録する",
		Long: strings.TrimSpace(`
導入直後に溜まっているレビュー依頼が一斉に発火するのを防ぎます。
daemon install の前に 1 回実行してください。
`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, _, err := g.open()
			if err != nil {
				return err
			}
			defer a.Close()

			report, err := a.Poll(cmd.Context(), app.PollOptions{Seed: true, Limit: limit})
			if err != nil {
				return err
			}
			printPollReport(cmd.OutOrStdout(), report, true)
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 0, "検索で見る PR の上限 (既定: 50)")
	return cmd
}

func newRunCmd(g *globals) *cobra.Command {
	var (
		repo     string
		prNumber int
		ruleName string
		mode     string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "指定した PR に対して手動で 1 回実行する",
		Long: strings.TrimSpace(`
処理済みかどうかを無視して実行します。失敗した run をやり直すときに使います。
対象はレビュー依頼中の PR に限られます。
`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, _, err := g.open()
			if err != nil {
				return err
			}
			defer a.Close()

			item, err := a.RunOne(cmd.Context(), app.RunOneOptions{
				Repo: repo, PRNumber: prNumber, Rule: ruleName, Mode: mode,
			})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s#%d  rule=%s  status=%s\n", item.Event.Repo, item.Event.PRNumber, item.Rule, item.Status)
			if item.OutputDir != "" {
				fmt.Fprintf(out, "出力: %s\n", item.OutputDir)
			}
			if item.Err != nil {
				return item.Err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "owner/name (必須)")
	cmd.Flags().IntVar(&prNumber, "pr", 0, "PR 番号 (必須)")
	cmd.Flags().StringVar(&ruleName, "rule", "", "使うルール名 (既定: 通常どおり先頭一致)")
	cmd.Flags().StringVar(&mode, "mode", "", "実行モードを上書きする (headless / ghostty)")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")
	return cmd
}

// printPollReport renders a poll's outcome. Details are only printed when the
// user asked for them, so a scheduled poll stays quiet in the launchd log.
func printPollReport(out interface{ Write([]byte) (int, error) }, report app.PollReport, detailed bool) {
	if report.Locked {
		fmt.Fprintln(out, "別の poll が実行中のためスキップしました")
		return
	}

	counts := report.Counts()
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[app.Outcome(k)]))
	}
	summary := strings.Join(parts, " ")
	if summary == "" {
		summary = "対象なし"
	}
	fmt.Fprintf(out, "検知 %d 件: %s\n", report.Detected, summary)

	if !detailed {
		return
	}
	for _, item := range report.Items {
		fmt.Fprintf(out, "  %-18s %s#%d", item.Outcome, item.Event.Repo, item.Event.PRNumber)
		if item.Rule != "" {
			fmt.Fprintf(out, " rule=%s", item.Rule)
		}
		if item.Status != "" {
			fmt.Fprintf(out, " status=%s", item.Status)
		}
		fmt.Fprintln(out)
		if item.Err != nil {
			fmt.Fprintf(out, "      error: %v\n", item.Err)
		}
		if item.Outcome == app.OutcomeNoRule {
			for _, d := range item.Decisions {
				fmt.Fprintf(out, "      %s: %s\n", d.Rule, d.Reason)
			}
		}
	}
}
