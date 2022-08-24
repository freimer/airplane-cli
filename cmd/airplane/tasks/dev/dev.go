package dev

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MakeNowJust/heredoc"
	"github.com/airplanedev/cli/cmd/airplane/auth/login"
	"github.com/airplanedev/cli/cmd/airplane/tasks/dev/config"
	viewsdev "github.com/airplanedev/cli/cmd/airplane/views/dev"
	"github.com/airplanedev/cli/pkg/analytics"
	"github.com/airplanedev/cli/pkg/cli"
	"github.com/airplanedev/cli/pkg/conf"
	"github.com/airplanedev/cli/pkg/dev"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/params"
	"github.com/airplanedev/cli/pkg/resource"
	"github.com/airplanedev/cli/pkg/server"
	"github.com/airplanedev/cli/pkg/utils"
	"github.com/airplanedev/lib/pkg/deploy/discover"
	"github.com/airplanedev/lib/pkg/utils/fsx"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type taskDevConfig struct {
	root          *cli.Config
	port          int
	devConfigPath string
	envSlug       string

	// Short-lived dev command fields
	// TODO: Remove these fields once launching the dev server is the default behavior of airplane dev
	fileOrDir string
	args      []string
	// If there are multiple tasks a, b in file f (config as code), specifying airplane
	// dev f::a would set fileOrDir to f and entrypointFunc to a.
	entrypointFunc string

	// Airplane dev server-related fields
	editor bool
}

func New(c *cli.Config) *cobra.Command {
	var cfg = taskDevConfig{root: c}

	cmd := &cobra.Command{
		Use:   "dev ./path/to/file",
		Short: "Locally run a task",
		Long:  "Locally runs a task, optionally with specific parameters.",
		Example: heredoc.Doc(`
			airplane dev ./task.js [-- <parameters...>]
			airplane dev ./task.ts::<exportName> [-- <parameters...>] (for multiple tasks in one file)
		`),
		PersistentPreRunE: utils.WithParentPersistentPreRunE(func(cmd *cobra.Command, args []string) error {
			// TODO: update the `dev` command to work w/out internet access
			return login.EnsureLoggedIn(cmd.Root().Context(), c)
		}),
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return errors.Wrap(err, "error determining current working directory")
			}
			if cfg.editor {
				// TODO: Support multiple dev server roots
				if len(args) == 0 {
					cfg.fileOrDir = wd
				} else if len(args) > 1 {
					return errors.New("Multiple dev server roots detected, please supply only one directory to discover tasks and views")
				} else {
					cfg.fileOrDir = args[0]
				}
			} else if len(args) == 0 || strings.HasPrefix(args[0], "-") {
				cfg.fileOrDir = wd
				cfg.args = args
			} else {
				cfg.fileOrDir = args[0]
				cfg.args = args[1:]
			}

			fileAndFunction := strings.Split(cfg.fileOrDir, "::")
			if len(fileAndFunction) > 1 {
				cfg.fileOrDir = fileAndFunction[0]
				cfg.entrypointFunc = fileAndFunction[1]
			}

			if cfg.devConfigPath == "" {
				// Check if default dev config exists
				if _, err := os.Stat(conf.DefaultDevConfigFileName); err == nil {
					cfg.devConfigPath = conf.DefaultDevConfigFileName
				}
			}

			return run(cmd.Root().Context(), cfg)
		},
	}

	cmd.AddCommand(config.New(c))

	cmd.Flags().StringVar(&cfg.envSlug, "env", "", "The slug of the environment to query. Defaults to your team's default environment.")
	cmd.Flags().IntVar(&cfg.port, "port", server.DefaultPort, "The port to start the local airplane api server on - defaults to 4000.")
	cmd.Flags().StringVar(&cfg.devConfigPath, "config-path", "", "The path to the dev config file to load into the local dev server.")
	// TODO: Make opening the editor the default behavior.
	cmd.PersistentFlags().BoolVar(&cfg.editor, "editor", false, "Run the local airplane editor")
	if err := cmd.PersistentFlags().MarkHidden("editor"); err != nil {
		logger.Debug("error: %s", err)
	}
	return cmd
}

func run(ctx context.Context, cfg taskDevConfig) error {
	if cfg.editor {
		return runLocalDevServer(ctx, cfg)
	}

	if !fsx.Exists(cfg.fileOrDir) {
		return errors.Errorf("Unable to open: %s", cfg.fileOrDir)
	}

	fileInfo, err := os.Stat(cfg.fileOrDir)
	if err != nil {
		return errors.Wrapf(err, "describing %s", cfg.fileOrDir)
	}

	if fileInfo.IsDir() {
		if viewsdev.IsView(cfg.fileOrDir) == nil {
			return viewsdev.Run(ctx, viewsdev.Config{
				Root:      cfg.root,
				FileOrDir: cfg.fileOrDir,
				Args:      cfg.args,
				EnvSlug:   cfg.envSlug,
			})
		}
		return errors.Errorf("%s is a directory", cfg.fileOrDir)
	}

	localExecutor := &dev.LocalExecutor{}
	// The API client is set in the root command, and defaults to api.airplane.dev as the host for deploys, etc. For
	// local dev, we send requests to a locally running api server, and so we override the host here.
	cfg.root.Client.Host = fmt.Sprintf("127.0.0.1:%d", cfg.port)

	var devConfig *conf.DevConfig
	var devConfigLoaded bool
	if cfg.devConfigPath != "" {
		devConfig, err = conf.ReadDevConfig(cfg.devConfigPath)
		if err != nil {
			// Attempt to create dev config file
			if errors.Is(err, conf.ErrMissing) {
				if path, creationErr := conf.PromptDevConfigFileCreation(cfg.devConfigPath); creationErr != nil {
					logger.Warning("Unable to create dev config file: %v", creationErr)
				} else {
					devConfig, err = conf.ReadDevConfig(path)
					if err != nil {
						return err
					} else {
						devConfigLoaded = true
					}
				}
			} else {
				logger.Warning("Unable to read dev config, using empty config")
			}
		} else {
			devConfigLoaded = true
		}
	}

	if devConfigLoaded {
		logger.Log("Loaded dev config file at %s", cfg.devConfigPath)
	} else {
		devConfig = &conf.DevConfig{}
	}

	apiServer, err := server.Start(server.Options{
		CLI:       cfg.root,
		EnvSlug:   cfg.envSlug,
		Executor:  localExecutor,
		Port:      cfg.port,
		DevConfig: devConfig,
	})
	if err != nil {
		return errors.Wrap(err, "starting local dev api server")
	}

	defer func() {
		if err := apiServer.Stop(context.Background()); err != nil {
			logger.Error("failed to stop local api server: %+v", err)
		}
	}()

	// Discover local tasks in the directory of the file.
	d := &discover.Discoverer{
		TaskDiscoverers: []discover.TaskDiscoverer{
			&discover.DefnDiscoverer{
				Client: cfg.root.Client,
			},
			&discover.CodeTaskDiscoverer{
				Client: cfg.root.Client,
			},
		},
		EnvSlug: cfg.envSlug,
		Client:  cfg.root.Client,
	}
	taskConfigs, viewConfigs, err := d.Discover(ctx, filepath.Dir(cfg.fileOrDir))
	if err != nil {
		return errors.Wrap(err, "discovering task configs")
	}
	taskConfig, err := getLocalDevTaskConfig(taskConfigs, cfg)
	if err != nil {
		return err
	}

	// TODO: Allow users to re-register tasks once we move to a long-running local api server
	if _, err := apiServer.RegisterTasksAndViews(taskConfigs, viewConfigs); err != nil {
		return err
	}
	paramValues, err := params.CLI(cfg.args, taskConfig.Def.GetName(), taskConfig.Def.GetParameters())
	if errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}

	resources, err := resource.GenerateAliasToResourceMap(taskConfig.Def.GetResourceAttachments(), devConfig.Resources)
	if err != nil {
		return errors.Wrap(err, "generating alias to resource map")
	}

	kind, kindOptions, err := dev.GetKindAndOptions(taskConfig)
	if err != nil {
		return err
	}

	executionConfig := dev.ExecutionConfig{
		ID:          server.GenerateRunID(),
		Name:        taskConfig.Def.GetName(),
		Kind:        kind,
		KindOptions: kindOptions,
		ParamValues: paramValues,
		Port:        cfg.port,
		Root:        cfg.root,
		File:        cfg.fileOrDir,
		Slug:        taskConfig.Def.GetSlug(),
		EnvSlug:     cfg.envSlug,
		Env:         devConfig.Env,
		Resources:   resources,
	}
	_, err = localExecutor.Execute(ctx, executionConfig)
	if err != nil {
		return errors.Wrap(err, "executing task")
	}

	analytics.Track(cfg.root, "Run Executed Locally", map[string]interface{}{
		"kind":         kind,
		"task_slug":    taskConfig.Def.GetSlug(),
		"task_name":    taskConfig.Def.GetName(),
		"env_slug":     cfg.envSlug,
		"num_params":   len(paramValues),
		"num_env_vars": len(devConfig.Env),
	}, analytics.TrackOpts{
		SkipSlack: true,
	})

	return nil
}

func getLocalDevTaskConfig(taskConfigs []discover.TaskConfig, cfg taskDevConfig) (discover.TaskConfig, error) {
	absPath, err := filepath.Abs(cfg.fileOrDir)
	if err != nil {
		return discover.TaskConfig{}, errors.Wrap(err, "converting file to absolute")
	}
	var potentialTaskConfigs []discover.TaskConfig
	for _, taskConfig := range taskConfigs {
		if taskConfig.Def.GetDefnFilePath() == absPath || taskConfig.TaskEntrypoint == absPath {
			potentialTaskConfigs = append(potentialTaskConfigs, taskConfig)
		}
	}

	if len(potentialTaskConfigs) == 0 {
		return discover.TaskConfig{}, errors.New("unable to find any task in file")
	}

	if len(potentialTaskConfigs) == 1 && cfg.entrypointFunc == "" {
		return potentialTaskConfigs[0], nil
	}

	for _, taskConfig := range potentialTaskConfigs {
		buildConfig, err := taskConfig.Def.GetBuildConfig()
		if err != nil {
			return discover.TaskConfig{}, errors.Wrap(err, "getting build config")
		}
		entrypointFunc, _ := buildConfig["entrypointFunc"].(string)

		if cfg.entrypointFunc == "" && entrypointFunc == "default" {
			return taskConfig, nil
		} else if cfg.entrypointFunc == entrypointFunc {
			return taskConfig, nil
		}
	}

	return discover.TaskConfig{}, errors.New("unable to find specified task in file")
}
