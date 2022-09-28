package dev

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/cli"
	"github.com/airplanedev/cli/pkg/conf"
	"github.com/airplanedev/cli/pkg/dev/logs"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/print"
	"github.com/airplanedev/cli/pkg/utils/pointers"
	"github.com/airplanedev/lib/pkg/build"
	"github.com/airplanedev/lib/pkg/builtins"
	"github.com/airplanedev/lib/pkg/deploy/discover"
	"github.com/airplanedev/lib/pkg/deploy/taskdir"
	"github.com/airplanedev/lib/pkg/deploy/taskdir/definitions"
	"github.com/airplanedev/lib/pkg/outputs"
	"github.com/airplanedev/lib/pkg/resources"
	"github.com/airplanedev/lib/pkg/runtime"
	"github.com/airplanedev/lib/pkg/utils/bufiox"
	"github.com/airplanedev/lib/pkg/utils/fsx"
	"github.com/airplanedev/ojson"
	"github.com/joho/godotenv"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// Executor is an interface that contains methods for executing task code.
type Executor interface {
	Execute(ctx context.Context, config LocalRunConfig) (api.Outputs, error)
}

// LocalExecutor is an implementation of Executor that runs task code locally.
type LocalExecutor struct{}

// LocalRunConfig is a struct that contains the necessary configs for running a task locally.
type LocalRunConfig struct {
	ID          string
	Name        string
	Kind        build.TaskKind
	KindOptions build.KindOptions
	ParamValues api.Values
	Port        int
	Root        *cli.Config
	File        string
	Slug        string
	EnvID       string
	EnvSlug     string
	ParentRunID *string
	Env         map[string]string
	ConfigVars  map[string]string
	AuthInfo    api.AuthInfoResponse
	// Mapping from alias to resource
	Resources map[string]resources.Resource
	IsBuiltin bool
	LogBroker logs.LogBroker
}

type CmdConfig struct {
	cmd        *exec.Cmd
	closer     io.Closer
	entrypoint string
	runtime    runtime.Interface
}

var LogIDGen IDGenerator

// Cmd returns the command needed to execute the task locally
func (l *LocalExecutor) Cmd(ctx context.Context, runConfig LocalRunConfig) (CmdConfig, error) {
	if runConfig.IsBuiltin {
		builtinClient, err := builtins.NewLocalClient(goruntime.GOOS, goruntime.GOARCH, logger.NewStdErrLogger(logger.StdErrLoggerOpts{}))
		if err != nil {
			logger.Error(err.Error())
			return CmdConfig{}, err
		}
		req, err := builtins.MarshalRequest(runConfig.Slug, runConfig.ParamValues)
		if err != nil {
			return CmdConfig{}, errors.New("invalid builtin request")
		}
		cmd, err := builtinClient.Cmd(ctx, req)
		if err != nil {
			return CmdConfig{}, err
		}
		return CmdConfig{cmd: cmd}, nil
	}
	entrypoint, err := entrypointFrom(runConfig.File)
	if err != nil && err != definitions.ErrNoEntrypoint {
		// REST tasks don't have an entrypoint, and it's not needed
		return CmdConfig{}, err
	}
	r, err := runtime.Lookup(entrypoint, runConfig.Kind)
	if err != nil {
		return CmdConfig{}, errors.Wrapf(err, "unsupported file type: %s", filepath.Base(entrypoint))
	}

	if !r.SupportsLocalExecution() {
		logger.Warning("Local execution is not supported for this task (kind=%s)", runConfig.Kind)
		return CmdConfig{}, nil
	}

	configVars := map[string]interface{}{}
	for k, v := range runConfig.ConfigVars {
		configVars[k] = v
	}

	cmds, closer, err := r.PrepareRun(ctx, logger.NewStdErrLogger(logger.StdErrLoggerOpts{}), runtime.PrepareRunOptions{
		Path:        entrypoint,
		ParamValues: runConfig.ParamValues,
		KindOptions: runConfig.KindOptions,
		ConfigVars:  configVars,
	})
	if err != nil {
		return CmdConfig{}, err
	}
	cmd := exec.CommandContext(ctx, cmds[0], cmds[1:]...)
	return CmdConfig{
		cmd:        cmd,
		closer:     closer,
		entrypoint: entrypoint,
		runtime:    r,
	}, nil
}

func (l *LocalExecutor) Execute(ctx context.Context, config LocalRunConfig) (api.Outputs, error) {
	cmdConfig, err := l.Cmd(ctx, config)
	if cmdConfig.closer != nil {
		defer cmdConfig.closer.Close()
	}
	if err != nil {
		return api.Outputs{}, err
	}
	cmd := cmdConfig.cmd
	r := cmdConfig.runtime
	entrypoint := cmdConfig.entrypoint
	print.BoxPrint(fmt.Sprintf("Locally running task [%s]", config.Slug))
	logger.Log("")

	logger.Debug("Running %s", logger.Bold(strings.Join(cmd.Args, " ")))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return api.Outputs{}, errors.Wrap(err, "stdout")
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return api.Outputs{}, errors.Wrap(err, "stderr")
	}

	// cmd.Env defaults to os.Environ _only if empty_. Since we add
	// to it, we need to also set it to os.Environ.
	cmd.Env = os.Environ()
	// only non builtins have a runtime
	if r != nil {
		// Load environment variables from dev config file:
		env, err := getDevEnvVars(r, entrypoint)
		if err != nil {
			return api.Outputs{}, err
		}
		// If environment variables are specified in the dev config file, use those instead
		if len(config.Env) > 0 {
			env = config.Env
		}
		for k, v := range env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	if cmd.Env, err = appendAirplaneEnvVars(cmd.Env, config); err != nil {
		return api.Outputs{}, errors.Wrap(err, "appending airplane-specific env vars")
	}
	if err := cmd.Start(); err != nil {
		return api.Outputs{}, errors.Wrap(err, "starting")
	}

	if config.LogBroker == nil {
		config.LogBroker = &logs.MockLogBroker{}
	}
	defer func() {
		config.LogBroker.Close()
	}()

	// mu guards o and chunks
	var mu sync.Mutex
	var o ojson.Value
	chunks := make(map[string]*strings.Builder)

	logParser := func(r io.Reader) error {
		scanner := bufiox.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			scanLogLine(config, line, &mu, &o, chunks)
			config.LogBroker.Record(api.LogItem{
				Timestamp: time.Now(),
				InsertID:  LogIDGen.Next(),
				Text:      line,
				Level:     "info",
			})
			logger.Log("[%s %s] %s", logger.Gray(config.Name), logger.Gray("log"), line)
		}
		return errors.Wrap(scanner.Err(), "scanning logs")
	}

	eg := errgroup.Group{}
	eg.Go(func() error {
		return logParser(stdout)
	})
	eg.Go(func() error {
		return logParser(stderr)
	})

	if err = eg.Wait(); err != nil {
		return api.Outputs{}, err
	}

	err = cmd.Wait()
	outputs := api.Outputs(o)
	logger.Log("")
	logger.Log("%s for task %s:", logger.Gray("Output"), logger.Gray(config.Slug))
	print.Outputs(outputs)

	logger.Log("")
	print.BoxPrint(fmt.Sprintf("Finished running task [%s]", config.Slug))
	logger.Log("")

	return outputs, err
}

func GetKindAndOptions(taskConfig discover.TaskConfig) (build.TaskKind, build.KindOptions, error) {
	kind, kindOptions, err := taskConfig.Def.GetKindAndOptions()
	if err != nil {
		return "", build.KindOptions{}, errors.Wrap(err, "getting kind and kind options")
	}

	buildConfig, err := taskConfig.Def.GetBuildConfig()
	if err != nil {
		return "", build.KindOptions{}, errors.Wrap(err, "getting build config")
	}
	if entrypointFunc, ok := buildConfig["entrypointFunc"]; ok {
		// Config as code depends on using the correct import name
		kindOptions["entrypointFunc"] = entrypointFunc
	}
	return kind, kindOptions, nil
}

func MaterializeEnvVars(taskConfig discover.TaskConfig, config *conf.DevConfig) (map[string]string, error) {
	envVars := map[string]string{}

	taskEnvVars, err := taskConfig.Def.GetEnv()
	if err != nil {
		return nil, err
	}

	for key, envVar := range taskEnvVars {
		if envVar.Value != nil {
			envVars[key] = *envVar.Value
		} else if envVar.Config != nil {
			if configVal, ok := config.ConfigVars[*envVar.Config]; !ok {
				logger.Warning("config %s is not defined in airplane.dev.yaml (referenced by env var %s)", *envVar.Config, key)
			} else {
				envVars[key] = configVal
			}
		}
	}
	return envVars, nil
}

func scanLogLine(config LocalRunConfig, line string, mu *sync.Mutex, o *ojson.Value, chunks map[string]*strings.Builder) {
	scanForErrors(config.Root, line)
	mu.Lock()
	defer mu.Unlock()
	parsed, err := outputs.Parse(chunks, line, outputs.ParseOptions{})
	if err != nil {
		logger.Error("[%s] %+v", logger.Gray("outputs"), err)
		return
	}
	if parsed != nil {
		err := outputs.ApplyOutputCommand(parsed, o)
		if err != nil {
			logger.Error("[%s] %+v", logger.Gray("outputs"), err)
			return
		}
	}
}

// getDevEnvVars will return a map of env vars, loading from .env and airplane.env
// files inside the task root.
//
// Env variables are first loaded by looking for any .env files between the root
// and entrypoint dir (inclusive). A second pass is done to look for airplane.env
// files. Env vars from successive files are merged in and overwrite duplicate keys.
func getDevEnvVars(r runtime.Interface, path string) (map[string]string, error) {
	root, err := r.Root(path)
	if err != nil {
		return nil, err
	}

	// dotenvs will contain a list of .env file paths that should be read.
	//
	// They will be loaded in order, with later .env files overwriting values
	// from earlier .env files.
	dotenvs := []string{}

	// Loop through directories from [workdir, root] inclusive, in reverse
	// order.
	dirs := []string{}
	for dir := filepath.Dir(path); dir != filepath.Dir(root); dir = filepath.Dir(dir) {
		dirs = append([]string{dir}, dirs...)
	}

	for _, file := range []string{".env", "airplane.env"} {
		for _, dir := range dirs {
			fp := filepath.Join(dir, file)
			if fsx.Exists(fp) {
				logger.Debug("Loading env vars from %s", logger.Bold(fp))
				dotenvs = append(dotenvs, fp)
			}
		}
	}

	if len(dotenvs) == 0 {
		return nil, nil
	}

	env, err := godotenv.Read(dotenvs...)
	return env, errors.Wrap(err, "reading .env")
}

// Returns an absolute entrypoint.
func entrypointFrom(file string) (string, error) {
	format := definitions.GetTaskDefFormat(file)
	switch format {
	case definitions.DefFormatYAML, definitions.DefFormatJSON:
		return entrypointFromDefn(file)
	default:
		path, err := filepath.Abs(file)
		if err != nil {
			return "", errors.Wrapf(err, "absolute path of %s", file)
		}
		return path, nil
	}
}

func entrypointFromDefn(file string) (string, error) {
	dir, err := taskdir.Open(file)
	if err != nil {
		return "", err
	}
	defer dir.Close()

	def, err := dir.ReadDefinition()
	if err != nil {
		return "", err
	}

	return def.GetAbsoluteEntrypoint()
}

func appendAirplaneEnvVars(env []string, config LocalRunConfig) ([]string, error) {
	env = append(env, fmt.Sprintf("AIRPLANE_API_HOST=http://127.0.0.1:%d", config.Port))
	env = append(env, "AIRPLANE_RESOURCES_VERSION=2")

	var runnerID, runnerEmail string
	if config.AuthInfo.User != nil {
		runnerID = config.AuthInfo.User.ID
		runnerEmail = config.AuthInfo.User.Email
	}

	var teamID string
	if config.AuthInfo.Team != nil {
		teamID = config.AuthInfo.Team.ID
	}

	// Environment variables documented in https://docs.airplane.dev/tasks/runtime-api-reference#environment-variables
	// We omit:
	// - AIRPLANE_REQUESTER_EMAIL
	// - AIRPLANE_REQUESTER_ID
	// - AIRPLANE_SESSION_ID
	// - AIRPLANE_TASK_REVISION_ID
	// - AIRPLANE_TRIGGER_ID
	// because there is no requester, session, task revision, or triggers in the context of local dev.
	env = append(env,
		fmt.Sprintf("AIRPLANE_ENV_ID=%s", config.EnvID),
		fmt.Sprintf("AIRPLANE_ENV_SLUG=%s", config.EnvSlug),
		fmt.Sprintf("AIRPLANE_RUN_ID=%s", config.ID),
		fmt.Sprintf("AIRPLANE_PARENT_RUN_ID=%s", pointers.ToString(config.ParentRunID)),
		fmt.Sprintf("AIRPLANE_RUNNER_EMAIL=%s", runnerEmail),
		fmt.Sprintf("AIRPLANE_RUNNER_ID=%s", runnerID),
		"AIRPLANE_RUNTIME=dev",
		fmt.Sprintf("AIRPLANE_TASK_ID=%s", config.Slug), // For local dev, we use the task's slug as its id.
		fmt.Sprintf("AIRPLANE_TEAM_ID=%s", teamID),
	)

	token, err := GenerateInsecureAirplaneToken(AirplaneTokenClaims{
		RunID: config.ID,
	})
	if err != nil {
		return nil, err
	}
	env = append(env, fmt.Sprintf("AIRPLANE_TOKEN=%s", token))

	serialized, err := json.Marshal(config.Resources)
	if err != nil {
		return nil, errors.Wrap(err, "marshaling resources")
	}
	env = append(env, fmt.Sprintf("AIRPLANE_RESOURCES=%s", string(serialized)))
	return env, nil
}
