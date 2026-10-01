package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func newStatusCmd(g *globals) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "status",
		Short: "直近の実行履歴を表示する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, _, err := g.open()
			if err != nil {
				return err
			}
			defer a.Close()

			runs, err := a.Store().RecentRuns(cmd.Context(), limit)
			if err != nil {
				return err
			}
			if len(runs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "実行履歴はまだありません")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "STARTED\tSTATUS\tPR\tRULE\tTOOK\tOUTPUT")
			for _, run := range runs {
				took := "-"
				if run.FinishedAt != nil {
					took = run.FinishedAt.Sub(run.StartedAt).Round(time.Second).String()
				}
				fmt.Fprintf(w, "%s\t%s\t%s#%d\t%s\t%s\t%s\n",
					run.StartedAt.Format("01-02 15:04"), run.Status,
					run.Repo, run.PRNumber, run.Rule, took, run.OutputDir)
			}
			return w.Flush()
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "表示件数")
	return cmd
}
