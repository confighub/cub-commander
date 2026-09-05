// Package cmd is the command tree. `cub commander` opens the TUI (M2); the
// hidden -e flag runs statements for scripts and tests.
package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/confighub/cub-commander/internal/catalog"
	"github.com/confighub/cub-commander/internal/cubclient"
	"github.com/confighub/cub-commander/internal/exec"
	"github.com/confighub/cub-commander/internal/format"
	"github.com/confighub/cub-commander/internal/lang"
	"github.com/confighub/cub-commander/internal/plan"
	"github.com/confighub/cub-commander/internal/rollout"
	"github.com/confighub/cub-commander/internal/tui"
)

var version = "dev"

func Version() string { return version }

var (
	flagExec   string
	flagSpace  string
	flagOutput string
	flagNoHdr  bool
)

var root = &cobra.Command{
	Use:           "commander",
	Short:         "Terminal lab for the ConfigHub data model",
	Long:          "cub commander opens a full-screen terminal for querying, browsing and saving views over ConfigHub.\nSee docs/design.md.",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if flagExec != "" {
			return runStatements(cmd.Context(), flagExec)
		}
		return tui.Run(plan.Session{Space: flagSpace})
	},
}

func init() {
	root.Flags().StringVarP(&flagExec, "execute", "e", "", "run statements and exit")
	root.Flags().StringVar(&flagSpace, "space", "*", "session space scope: a space slug, or '*' for the whole org (USE changes it later)")
	root.Flags().StringVarP(&flagOutput, "output", "o", "table", "table, json or csv")
	root.Flags().BoolVar(&flagNoHdr, "no-headers", false, "omit the header line")
	_ = root.Flags().MarkHidden("execute")
	root.AddCommand(&cobra.Command{Use: "version", Short: "Print the version", Run: func(*cobra.Command, []string) { fmt.Println(version) }})
}

func Execute() {
	if err := root.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runStatements(ctx context.Context, src string) error {
	stmts, err := lang.Parse(src)
	if err != nil {
		return err
	}
	sess := plan.Session{Space: flagSpace}
	var client *cubclient.Client
	getClient := func() (*cubclient.Client, error) {
		if client == nil {
			client, err = cubclient.New()
		}
		return client, err
	}
	for _, st := range stmts {
		switch x := st.(type) {
		case *lang.UseStmt:
			if x.Org {
				sess.Space = "*"
			} else {
				sess.Space = x.Space
			}
		case *lang.ExplainStmt:
			sel, ok := x.Inner.(*lang.SelectStmt)
			if !ok {
				return fmt.Errorf("EXPLAIN only explains SELECT")
			}
			p, err := plan.Compile(sel, sess)
			if err != nil {
				return err
			}
			spaceID := ""
			if p.List.Space != "" {
				if c, err := getClient(); err == nil {
					spaceID, _ = c.SpaceID(ctx, p.List.Space)
				}
			}
			fmt.Print(p.Explain(spaceID))
		case *lang.SelectStmt:
			p, err := plan.Compile(x, sess)
			if err != nil {
				return err
			}
			c, err := getClient()
			if err != nil {
				return err
			}
			if p.Diff != nil {
				d, err := exec.RunDiff(ctx, c, p)
				if err != nil {
					return err
				}
				res := &exec.Result{Headers: []string{"PAIR", "STATUS", "A", "B"}}
				for _, pr := range d.Pairs {
					names := func(rows []cubclient.Row) string {
						var out []string
						for _, r := range rows {
							out = append(out, exec.RowName(r))
						}
						return strings.Join(out, " ")
					}
					res.Rows = append(res.Rows, []any{pr.Key, pr.Status, names(pr.A), names(pr.B)})
				}
				if err := output(res); err != nil {
					return err
				}
				keys := make([]string, len(d.By))
				for i, k := range d.By {
					keys[i] = k.Path
				}
				fmt.Fprintf(os.Stderr, "%d pairs: %d differ, %d same, %d only-a, %d only-b, %d multi; paired by %s\n", len(d.Pairs), d.Counts["differ"], d.Counts["same"], d.Counts["only-a"], d.Counts["only-b"], d.Counts["multi"], strings.Join(keys, ", "))
				continue
			}
			rows, err := exec.List(ctx, c, p)
			if err != nil {
				return err
			}
			if p.Rollout != nil {
				if len(rows) != 1 {
					return fmt.Errorf("rollout opens one change order; the where steps matched %d", len(rows))
				}
				ro, err := rollout.Load(ctx, c, rollout.NewCache(), rows[0])
				if err != nil {
					return err
				}
				fmt.Print(ro.Text())
				continue
			}
			if p.RolloutCols {
				if _, err := tui.RolloutRunner(ctx, c, x, p, rows); err != nil {
					return err
				}
			}
			res, err := exec.Local(p, rows)
			if err != nil {
				return err
			}
			if err := output(res); err != nil {
				return err
			}
		case *lang.ShowStmt:
			if err := show(x); err != nil {
				return err
			}
		case *lang.DescribeStmt:
			body, err := tui.Describe(x.Name)
			if err != nil {
				return err
			}
			fmt.Print(body)
		}
	}
	return nil
}

func output(res *exec.Result) error {
	switch flagOutput {
	case "json":
		return format.JSON(os.Stdout, res)
	case "csv":
		return format.CSV(os.Stdout, res)
	default:
		format.Table(os.Stdout, res, !flagNoHdr)
	}
	return nil
}

func show(s *lang.ShowStmt) error {
	live := catalog.NewLive()
	switch s.What {
	case "LABELS", "VALUES", "SPACES":
		c, err := cubclient.New()
		if err != nil {
			return err
		}
		if err := live.Sample(context.Background(), c); err != nil {
			return err
		}
	}
	res, err := tui.ShowResult(s, live)
	if err != nil {
		return err
	}
	return output(res)
}
