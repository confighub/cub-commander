// Command cub-commander is a cub plugin: a full-screen terminal lab for the
// ConfigHub data model. See docs/design.md.
//
//	make plugin     (wraps: cub plugin install ./bin/cub-commander)
//	cub commander   (the TUI; arrives in M2)
//	cub commander -e "SELECT Slug, Space.Slug FROM Unit IN * WHERE Labels.Environment = 'prod'"
package main

import (
	"fmt"
	"os"

	"github.com/confighub/sdk/core/plugin"

	"github.com/confighub/cub-commander/cmd"
)

func main() {
	manifest := plugin.Manifest{
		Name:    "commander",
		Version: cmd.Version(),
		Commands: []plugin.Command{{
			Name:    "commander",
			Aliases: []string{"cmdr"},
			Summary: "Terminal lab for the ConfigHub data model",
		}},
	}
	if handled, err := plugin.HandleHook(manifest); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	cmd.Execute()
}
