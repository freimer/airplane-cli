package delete_resource

import (
	"context"

	"github.com/MakeNowJust/heredoc"
	"github.com/airplanedev/cli/pkg/cli"
	"github.com/airplanedev/cli/pkg/conf"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type config struct {
	devCLI *cli.DevCLI
	slug   string
}

func New(c *cli.DevCLI) *cobra.Command {
	var cfg = config{devCLI: c}
	cmd := &cobra.Command{
		Use:   "delete-resource",
		Short: "Deletes a resource with the given slug from the dev config file",
		Example: heredoc.Doc(`
			airplane dev config delete-resource <slug>
			airplane dev config delete-resource <slug1> <slug2> ...
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.slug = args[0]
			return run(cmd.Root().Context(), cfg)
		},
	}

	return cmd
}

func run(ctx context.Context, cfg config) error {
	devConfig := cfg.devCLI.DevConfig
	if devConfig.Resources != nil {
		if _, ok := devConfig.Resources[cfg.slug]; ok {
			delete(devConfig.Resources, cfg.slug)
			if err := conf.WriteDevConfig(cfg.devCLI.Filepath, devConfig); err != nil {
				return err
			}

			logger.Log("Deleted resource with slug `%s` from dev config file.", cfg.slug)
			return nil
		}
	}

	return errors.Errorf("Resource with slug `%s` does not exist in dev config file", cfg.slug)
}
