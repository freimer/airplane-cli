package dev

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/dev"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/server"
	"github.com/airplanedev/lib/pkg/deploy/discover"
	"github.com/pkg/errors"
)

func runLocalDevServer(ctx context.Context, cfg taskDevConfig) error {
	// The API client is set in the root command, and defaults to api.airplane.dev as the host for deploys, etc. For
	// local dev, we send requests to a locally running api server, and so we create an api client that hits localhost
	// here.
	localClient := &api.Client{
		Host:   fmt.Sprintf("127.0.0.1:%d", cfg.port),
		Token:  cfg.root.Client.Token,
		Source: cfg.root.Client.Source,
		APIKey: cfg.root.Client.APIKey,
		TeamID: cfg.root.Client.TeamID,
	}

	authInfo, err := cfg.root.Client.AuthInfo(ctx)
	if err != nil {
		return err
	}

	env, err := cfg.root.Client.GetEnv(ctx, cfg.envSlug)
	if err != nil {
		return err
	}

	localExecutor := &dev.LocalExecutor{}
	// Use absolute path to dev root to allow the local dev server to more easily calculate relative paths.
	dir := filepath.Dir(cfg.fileOrDir)
	absoluteDir, err := filepath.Abs(dir)
	if err != nil {
		return errors.Wrap(err, "getting absolute directory of dev server root")
	}
	apiServer, err := server.Start(server.Options{
		CLI:         cfg.root,
		LocalClient: localClient,
		DevConfig:   cfg.devConfig,
		EnvID:       env.ID,
		EnvSlug:     env.Slug,
		Executor:    localExecutor,
		Port:        cfg.port,
		Dir:         absoluteDir,
		AuthInfo:    authInfo,
	})
	if err != nil {
		return errors.Wrap(err, "starting local dev server")
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	logger.Log("Discovering tasks and views...")

	l := logger.NewStdErrLogger(logger.StdErrLoggerOpts{})
	// Discover local tasks and views in the directory of the file.
	d := &discover.Discoverer{
		TaskDiscoverers: []discover.TaskDiscoverer{
			&discover.DefnDiscoverer{
				Client: localClient,
				Logger: l,
			},
			&discover.CodeTaskDiscoverer{
				Client: localClient,
				Logger: l,
			},
		},
		ViewDiscoverers: []discover.ViewDiscoverer{
			&discover.ViewDefnDiscoverer{
				Client: localClient,
				Logger: l,
			},
			&discover.CodeViewDiscoverer{
				Client: localClient,
				Logger: l,
			},
		},
		EnvSlug: cfg.envSlug,
		Client:  localClient,
	}

	taskConfigs, viewConfigs, err := d.Discover(ctx, cfg.fileOrDir)
	if err != nil {
		return errors.Wrap(err, "discovering task configs")
	}

	// Print out discovered views and tasks to the user
	taskNoun := "tasks"
	if len(taskConfigs) == 1 {
		taskNoun = "task"
	}
	logger.Log("Found %d %s:", len(taskConfigs), taskNoun)
	for _, task := range taskConfigs {
		logger.Log("- %s", task.Def.GetName())
	}

	logger.Log("")

	viewNoun := "views"
	if len(viewConfigs) == 1 {
		viewNoun = "view"
	}
	logger.Log("Found %d %s:", len(viewConfigs), viewNoun)
	for _, view := range viewConfigs {
		logger.Log("- %s", view.Def.Name)
	}

	// Register discovered tasks with local dev server
	warnings, err := apiServer.RegisterTasksAndViews(taskConfigs, viewConfigs)
	if err != nil {
		return err
	}
	if len(warnings.UnsupportedApps) > 0 {
		logger.Log(" ")
		logger.Log("Skipping %v unsupported tasks or views:", len(warnings.UnsupportedApps))
		for _, app := range warnings.UnsupportedApps {
			logger.Log("- %s: %s", app.Name, app.Reason)
		}
	}

	if len(warnings.UnattachedResources) > 0 {
		logger.Log(" ")
		logger.Log(
			"The following tasks have resource attachments that are not defined in the dev config file. Please " +
				"add them through the previewer or run `airplane dev config set-resource`.")
		for _, ur := range warnings.UnattachedResources {
			logger.Log("- %s: %s", ur.TaskName, ur.ResourceSlugs)
		}
	}

	logger.Log("")
	logger.Log("Visit %s/editor?host=http://localhost:%d for a development UI.", cfg.root.Client.AppURL(), cfg.port)
	logger.Log("[Ctrl+C] to shutdown the local dev server.")

	// Wait for termination signal (e.g. Ctrl+C)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := apiServer.Stop(ctx); err != nil {
		return errors.Wrap(err, "stopping api server")
	}

	return nil
}
