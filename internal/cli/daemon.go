package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/YasushiKobayashi/agent-dispatch/internal/daemon"
)

func newDaemonCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "launchd への登録を管理する",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(newDaemonInstallCmd(g), newDaemonUninstallCmd(), newDaemonStatusCmd(), newDaemonPlistCmd(g))
	return cmd
}

// daemonOptions builds the launchd options from the current configuration.
func (g *globals) daemonOptions(interval int) (daemon.Options, error) {
	cfg, _, err := g.load()
	if err != nil {
		return daemon.Options{}, err
	}

	binary, err := os.Executable()
	if err != nil {
		return daemon.Options{}, fmt.Errorf("locate the reviewd binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		binary = resolved
	}

	return daemon.Options{
		BinaryPath: binary,
		ConfigPath: g.configPath,
		RulesPath:  g.rulesPath,
		LogsDir:    cfg.Paths.LogsDir,
		Interval:   interval,
		// git is needed for the worktree, gh for detection, claude for the run.
		PathEnv: daemon.DefaultPath(cfg.GitHub.GhPath, cfg.Claude.Path, "git"),
		// PATH alone is not enough when those are version-manager shims.
		Env: daemon.CollectEnv(),
	}, nil
}

func newDaemonInstallCmd(g *globals) *cobra.Command {
	var interval int

	cmd := &cobra.Command{
		Use:   "install",
		Short: "launchd に登録して常駐させる",
		Long: strings.TrimSpace(`
~/Library/LaunchAgents/com.yasushikobayashi.reviewd.plist を書き出して読み込みます。

PATH は install した時点で解決したものを plist に焼き込みます。
launchd の既定 PATH には aqua / Homebrew / mise の shim が入らず、
gh・claude・git が見つからないのが最頻の詰まりどころのためです。
gh や claude を入れ替えたら install し直してください。

初回は先に "reviewd seed" を実行して、溜まっているレビュー依頼が
一斉に発火しないようにしてください。
`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := g.daemonOptions(interval)
			if err != nil {
				return err
			}
			path, err := daemon.Install(opts)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "登録しました: %s\n", path)
			fmt.Fprintf(out, "  実行間隔: %d 秒\n", opts.Interval)
			fmt.Fprintf(out, "  ログ:     %s\n", opts.LogsDir)
			fmt.Fprintf(out, "  PATH:     %s\n", opts.PathEnv)
			return nil
		},
	}

	cmd.Flags().IntVar(&interval, "interval", 180, "poll の実行間隔（秒）")
	return cmd
}

func newDaemonUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "launchd の登録を解除する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := daemon.Uninstall()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "解除しました: %s\n", path)
			return nil
		},
	}
}

func newDaemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "launchd から見た常駐状態を表示する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out, err := daemon.Status()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "未登録です (`reviewd daemon install` で登録できます)\n")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), out)
			return nil
		},
	}
}

// newDaemonPlistCmd prints the plist without touching launchd, so the user can
// see exactly what install would write.
func newDaemonPlistCmd(g *globals) *cobra.Command {
	var interval int

	cmd := &cobra.Command{
		Use:   "plist",
		Short: "install が書き出す plist を標準出力に表示する（書き込みはしない）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := g.daemonOptions(interval)
			if err != nil {
				return err
			}
			body, err := daemon.Plist(opts)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), body)
			return nil
		},
	}

	cmd.Flags().IntVar(&interval, "interval", 180, "poll の実行間隔（秒）")
	return cmd
}
