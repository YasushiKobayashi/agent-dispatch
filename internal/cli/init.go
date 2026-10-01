package cli

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

//go:embed templates/config.yaml templates/rules.yaml
var templates embed.FS

func newInitCmd(g *globals) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "config.yaml と rules.yaml の雛形を作る",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			for _, f := range []struct{ name, dest string }{
				{"templates/config.yaml", g.configPath},
				{"templates/rules.yaml", g.rulesPath},
			} {
				written, err := writeTemplate(f.name, f.dest, force)
				if err != nil {
					return err
				}
				if written {
					fmt.Fprintf(out, "作成: %s\n", f.dest)
				} else {
					fmt.Fprintf(out, "既存のため据え置き: %s  (--force で上書き)\n", f.dest)
				}
			}
			fmt.Fprintln(out, "\n次: rules.yaml を編集 → `reviewd poll --dry-run` → `reviewd seed` → `reviewd daemon install`")
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "既存ファイルを上書きする")
	return cmd
}

func writeTemplate(name, dest string, force bool) (bool, error) {
	if !force {
		if _, err := os.Stat(dest); err == nil {
			return false, nil
		}
	}
	body, err := templates.ReadFile(name)
	if err != nil {
		return false, fmt.Errorf("read embedded %s: %w", name, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", filepath.Dir(dest), err)
	}
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", dest, err)
	}
	return true, nil
}
