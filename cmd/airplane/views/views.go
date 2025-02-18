package views

import (
	"github.com/MakeNowJust/heredoc"
	"github.com/airplanedev/cli/cmd/airplane/auth/login"
	"github.com/airplanedev/cli/cmd/airplane/tasks/deploy"
	"github.com/airplanedev/cli/cmd/airplane/views/dev"
	"github.com/airplanedev/cli/cmd/airplane/views/initcmd"
	"github.com/airplanedev/cli/pkg/cli"
	"github.com/airplanedev/cli/pkg/utils"
	"github.com/spf13/cobra"
)

// New returns a new cobra command.
func New(c *cli.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "views",
		Short:   "Manage views",
		Long:    "Manage views",
		Aliases: []string{"view"},
		Example: heredoc.Doc(`
			airplane views init
			airplane views dev
			airplane views deploy
		`),
		PersistentPreRunE: utils.WithParentPersistentPreRunE(func(cmd *cobra.Command, args []string) error {
			return login.EnsureLoggedIn(cmd.Root().Context(), c)
		}),
		Hidden: true,
	}

	cmd.AddCommand(deploy.New(c))
	cmd.AddCommand(dev.New(c))
	cmd.AddCommand(initcmd.New(c))

	return cmd
}
