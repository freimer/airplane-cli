package dev

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/airplanedev/cli/pkg/conf"
	"github.com/airplanedev/cli/pkg/dev"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/server"
	"github.com/airplanedev/lib/pkg/deploy/discover"
	"github.com/pkg/errors"
)

func runLocalDevServer(ctx context.Context, cfg taskDevConfig) error {
	appUrl := cfg.root.Client.AppURL()
	// The API client is set in the root command, and defaults to api.airplane.dev as the host for deploys, etc. For
	// local dev, we send requests to a locally running api server, and so we override the host here.
	cfg.root.Client.Host = fmt.Sprintf("127.0.0.1:%d", cfg.port)

	localExecutor := &dev.LocalExecutor{}
	var devConfig conf.DevConfig
	var err error

	if cfg.devConfigPath != "" {
		devConfig, err = conf.ReadDevConfig(cfg.devConfigPath)
		if err != nil {
			return errors.Wrap(err, "loading in dev config file")
		}
	}

	apiServer, err := server.Start(server.Options{
		CLI:       cfg.root,
		EnvSlug:   cfg.envSlug,
		Executor:  localExecutor,
		Port:      cfg.port,
		DevConfig: devConfig,
		Dir:       cfg.dir,
	})
	if err != nil {
		return errors.Wrap(err, "starting local dev server")
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	logger.Log("Discovering tasks and views...")

	// Discover local tasks and views in the directory of the file.
	d := &discover.Discoverer{
		TaskDiscoverers: []discover.TaskDiscoverer{
			&discover.DefnDiscoverer{
				Client: cfg.root.Client,
			},
		},
		EnvSlug: cfg.envSlug,
		Client:  cfg.root.Client,
	}
	d.ViewDiscoverers = append(d.ViewDiscoverers, &discover.ViewDefnDiscoverer{Client: cfg.root.Client})

	taskConfigs, viewConfigs, err := d.Discover(ctx, cfg.dir)
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
	apiServer.RegisterTasksAndViews(taskConfigs, viewConfigs)

	logger.Log("")
	logger.Log("Visit https://%s/editor?host=http://localhost:%d for a development UI.", appUrl.Host, cfg.port)
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
