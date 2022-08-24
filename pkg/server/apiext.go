package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/dev"
	"github.com/airplanedev/cli/pkg/dev/logs"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/print"
	"github.com/airplanedev/cli/pkg/resource"
	"github.com/airplanedev/cli/pkg/utils"
	libapi "github.com/airplanedev/lib/pkg/api"
	"github.com/airplanedev/lib/pkg/builtins"
	"github.com/gorilla/mux"
	"github.com/pkg/errors"
)

type LocalRun struct {
	RunID       string                 `json:"runID"`
	Status      api.RunStatus          `json:"status"`
	Outputs     api.Outputs            `json:"outputs"`
	CreatedAt   time.Time              `json:"createdAt"`
	CreatorID   string                 `json:"creatorID"`
	SucceededAt *time.Time             `json:"succeededAt"`
	FailedAt    *time.Time             `json:"failedAt"`
	ParamValues map[string]interface{} `json:"paramValues"`
	Parameters  *libapi.Parameters     `json:"parameters"`
	LogBroker   logs.LogBroker         `json:"-"`
	TaskName    string                 `json:"taskName"`
	Displays    []libapi.Display       `json:"displays"`
}

// NewLocalRun initializes a run for local dev.
func NewLocalRun() *LocalRun {
	return &LocalRun{
		Status:      api.RunQueued,
		ParamValues: map[string]interface{}{},
		CreatedAt:   time.Now(),
		LogBroker:   logs.NewDevLogBroker(),
		Displays:    []libapi.Display{},
	}
}

// AttachExternalAPIRoutes attaches a minimal subset of the actual Airplane API endpoints that are necessary to locally develop
// a task. For example, a workflow task might call airplane.execute, which would normally make a request to the
// /v0/tasks/execute endpoint in production, but instead we have our own implementation below.
func AttachExternalAPIRoutes(r *mux.Router, state *State) {
	const basePath = "/v0/"
	r = r.NewRoute().PathPrefix(basePath).Subrouter()

	r.Handle("/tasks/execute", HandlerWithBody(state, ExecuteTaskHandler)).Methods("POST", "OPTIONS")
	r.Handle("/tasks/getMetadata", Handler(state, GetTaskMetadataHandler)).Methods("GET", "OPTIONS")
	r.Handle("/tasks/get", Handler(state, GetTaskInfoHandler)).Methods("GET", "OPTIONS")
	r.Handle("/runs/getOutputs", Handler(state, GetOutputsHandler)).Methods("GET", "OPTIONS")
	r.Handle("/runs/get", Handler(state, GetRunHandler)).Methods("GET", "OPTIONS")
	r.Handle("/runs/list", Handler(state, ListRunsHandler)).Methods("GET", "OPTIONS")

	r.Handle("/resources/list", Handler(state, ListResourcesHandler)).Methods("GET", "OPTIONS")

	r.Handle("/views/get", Handler(state, GetViewHandler)).Methods("GET", "OPTIONS")

	r.Handle("/displays/list", Handler(state, ListDisplaysHandler)).Methods("GET", "OPTIONS")
	r.Handle("/displays/create", HandlerWithBody(state, CreateDisplayHandler)).Methods("POST", "OPTIONS")
}

type ExecuteTaskRequest struct {
	RunID       string            `json:"runID"`
	Slug        string            `json:"slug"`
	ParamValues api.Values        `json:"paramValues"`
	Resources   map[string]string `json:"resources"`
}

// ExecuteTaskHandler handles requests to the /v0/tasks/execute endpoint
func ExecuteTaskHandler(ctx context.Context, state *State, r *http.Request, req ExecuteTaskRequest) (LocalRun, error) {
	// Allow run IDs to be generated beforehand; this is needed so that the /dev/logs endpoint can start waiting
	// for logs for a given run before that run's execution has started.
	runID := req.RunID
	var run LocalRun
	if runID != "" {
		run, _ = state.runs.get(runID)
	} else {
		runID = GenerateRunID()
		run = *NewLocalRun()
	}

	localTaskConfig, ok := state.taskConfigs[req.Slug]
	isBuiltin := builtins.IsBuiltinTaskSlug(req.Slug)
	var parameters libapi.Parameters
	start := time.Now()
	if isBuiltin || ok {
		executionConfig := dev.ExecutionConfig{
			ID:          runID,
			ParamValues: req.ParamValues,
			Port:        state.port,
			Root:        state.cliConfig,
			Slug:        req.Slug,
			EnvSlug:     state.envSlug,
			IsBuiltin:   isBuiltin,
			LogBroker:   run.LogBroker,
		}
		resourceAttachments := map[string]string{}
		// Builtins have a specific alias in the form of "rest", "db", etc. that is required by the builtins binary,
		// and so we need to manually generate resource attachments.
		if isBuiltin {
			// The SDK should provide us with exactly one resource for builtins.
			if len(req.Resources) != 1 {
				return LocalRun{}, errors.Errorf("unable to determine resource required by builtin, there is not exactly one resource in request: %+v", req.Resources)
			}

			// Get the only entry in the request resource map.
			var builtinAlias, resourceID string
			for builtinAlias, resourceID = range req.Resources {
			}

			var foundResource bool
			for slug, res := range state.devConfig.Resources {
				if res.ID() == resourceID {
					resourceAttachments[builtinAlias] = slug
					foundResource = true
				}
			}

			if !foundResource {
				return LocalRun{}, errors.Errorf("resource with id %s not found in dev config file", resourceID)
			}
		} else if localTaskConfig.Def != nil {
			kind, kindOptions, err := dev.GetKindAndOptions(localTaskConfig)
			if err != nil {
				return LocalRun{}, err
			}
			executionConfig.Kind = kind
			executionConfig.KindOptions = kindOptions
			executionConfig.Name = localTaskConfig.Def.GetName()
			executionConfig.File = localTaskConfig.TaskEntrypoint
			resourceAttachments = localTaskConfig.Def.GetResourceAttachments()
			parameters = localTaskConfig.Def.GetParameters()

		}
		resources, err := resource.GenerateAliasToResourceMap(
			resourceAttachments,
			state.devConfig.Resources,
		)
		if err != nil {
			return LocalRun{}, errors.Wrap(err, "generating alias to resource map")
		}
		executionConfig.Resources = resources
		run.RunID = runID
		run.CreatedAt = start
		run.ParamValues = req.ParamValues
		run.Parameters = &parameters
		run.TaskName = req.Slug
		run.Status = api.RunActive
		// if the user is authenticated in CLI, use their ID
		run.CreatorID = state.cliConfig.ParseTokenForAnalytics().UserID
		state.runs.add(req.Slug, runID, run)

		outputs, err := state.executor.Execute(ctx, executionConfig)

		completedAt := time.Now()
		run, _ = state.runs.update(runID, func(run *LocalRun) {
			if err != nil {
				run.Status = api.RunFailed
				run.FailedAt = &completedAt
			} else {
				run.Status = api.RunSucceeded
				run.SucceededAt = &completedAt
			}
			run.Outputs = outputs
		})
	} else {
		logger.Error("task with slug %s is not registered locally", req.Slug)
	}

	return run, nil
}

// GetTaskMetadataHandler handles requests to the /v0/tasks/metadata endpoint. It generates a deterministic task ID for
// each task found locally, and its primary purpose is to ensure that the task discoverer does not error.
func GetTaskMetadataHandler(ctx context.Context, state *State, r *http.Request) (libapi.TaskMetadata, error) {
	slug := r.URL.Query().Get("slug")
	return libapi.TaskMetadata{
		ID:   fmt.Sprintf("tsk-%s", slug),
		Slug: slug,
	}, nil
}

// GetViewHandler handles requests to the /v0/views/get endpoint. It generates a deterministic view ID for each view
// found locally, and its primary purpose is to ensure that the view discoverer does not error.
func GetViewHandler(ctx context.Context, state *State, r *http.Request) (libapi.View, error) {
	slug := r.URL.Query().Get("slug")
	if slug == "" {
		return libapi.View{}, errors.New("slug cannot be empty")
	}

	return libapi.View{
		ID:   fmt.Sprintf("vew-%s", slug),
		Slug: r.URL.Query().Get("slug"),
	}, nil
}

// GetRunHandler handles requests to the /v0/runs/get endpoint
func GetRunHandler(ctx context.Context, state *State, r *http.Request) (LocalRun, error) {
	runID := r.URL.Query().Get("id")
	run, ok := state.runs.get(runID)
	if !ok {
		return LocalRun{}, errors.Errorf("run with id %s not found", runID)
	}
	return run, nil
}

type ListRunsResponse struct {
	Runs []LocalRun `json:"runs"`
}

func ListRunsHandler(ctx context.Context, state *State, r *http.Request) (ListRunsResponse, error) {
	taskSlug := r.URL.Query().Get("taskSlug")
	runs := state.runs.getRunHistory(taskSlug)
	return ListRunsResponse{
		Runs: runs,
	}, nil
}

type GetOutputsResponse struct {
	// Outputs from this run.
	Output api.Outputs `json:"output"`
}

// GetOutputsHandler handles requests to the /v0/runs/getOutputs endpoint
func GetOutputsHandler(ctx context.Context, state *State, r *http.Request) (GetOutputsResponse, error) {
	runID := r.URL.Query().Get("id")
	run, ok := state.runs.get(runID)
	if !ok {
		return GetOutputsResponse{}, errors.Errorf("run with id %s not found", runID)
	}

	return GetOutputsResponse{
		Output: run.Outputs,
	}, nil
}

// GetTaskInfoHandler handles requests to the /v0/tasks?slug=<task_slug> endpoint.
func GetTaskInfoHandler(ctx context.Context, state *State, r *http.Request) (libapi.UpdateTaskRequest, error) {
	taskSlug := r.URL.Query().Get("slug")
	if taskSlug == "" {
		return libapi.UpdateTaskRequest{}, errors.New("Task slug was not supplied, request path must be of the form /v0/tasks?slug=<task_slug>")
	}
	taskConfig, ok := state.taskConfigs[taskSlug]
	if !ok {
		return libapi.UpdateTaskRequest{}, errors.Errorf("Task with slug %s not found", taskSlug)
	}
	req, err := taskConfig.Def.GetUpdateTaskRequest(ctx, state.cliConfig.Client)
	if err != nil {
		logger.Error("Encountered error while getting task info: %v", err)
		return libapi.UpdateTaskRequest{}, errors.Errorf("error getting task %s", taskSlug)
	}
	return req, nil
}

type ListDisplaysResponse struct {
	Displays []libapi.Display `json:"displays"`
}

func ListDisplaysHandler(ctx context.Context, state *State, r *http.Request) (ListDisplaysResponse, error) {
	runID := r.URL.Query().Get("runID")
	run, ok := state.runs.get(runID)
	if !ok {
		return ListDisplaysResponse{}, errors.Errorf("run with id %q not found", runID)
	}

	return ListDisplaysResponse{
		Displays: append([]libapi.Display{}, run.Displays...),
	}, nil
}

type CreateDisplayRequest struct {
	Display libapi.Display `json:"display"`
}

type CreateDisplayResponse struct {
	Display libapi.Display `json:"display"`
}

func CreateDisplayHandler(ctx context.Context, state *State, r *http.Request, req CreateDisplayRequest) (CreateDisplayResponse, error) {
	token := r.Header.Get("X-Airplane-Token")
	if token == "" {
		return CreateDisplayResponse{}, errors.Errorf("expected a X-Airplane-Token header")
	}
	claims, err := dev.ParseInsecureAirplaneToken(token)
	if err != nil {
		return CreateDisplayResponse{}, errors.Errorf("invalid airplane token: %s", err.Error())
	}
	runID := claims.RunID

	now := time.Now()
	display := libapi.Display{
		ID:        "dsp" + utils.RandomString(10, utils.CharsetLowercaseNumeric),
		RunID:     runID,
		Kind:      req.Display.Kind,
		CreatedAt: now,
		UpdatedAt: now,
		Content:   req.Display.Content,
	}

	run, ok := state.runs.update(runID, func(run *LocalRun) {
		run.Displays = append(run.Displays, display)
	})
	if !ok {
		return CreateDisplayResponse{}, errors.Errorf("run with id %q not found", runID)
	}

	content := fmt.Sprintf("[kind=%s]\n\n%s", display.Kind, display.Content)
	prefix := "[" + logger.Gray(run.TaskName+" display") + "] "
	print.BoxPrintWithPrefix(content, prefix)

	return CreateDisplayResponse{
		Display: display,
	}, nil
}
