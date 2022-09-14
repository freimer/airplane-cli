package apiext

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/dev"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/print"
	"github.com/airplanedev/cli/pkg/resource"
	"github.com/airplanedev/cli/pkg/server/errorlib"
	"github.com/airplanedev/cli/pkg/server/handlers"
	"github.com/airplanedev/cli/pkg/server/state"
	"github.com/airplanedev/cli/pkg/utils"
	libapi "github.com/airplanedev/lib/pkg/api"
	"github.com/airplanedev/lib/pkg/builtins"
	"github.com/airplanedev/lib/pkg/resources/conversion"
	"github.com/gorilla/mux"
	"github.com/pkg/errors"
)

// AttachExternalAPIRoutes attaches a minimal subset of the actual Airplane API endpoints that are necessary to locally develop
// a task. For example, a workflow task might call airplane.execute, which would normally make a request to the
// /v0/tasks/execute endpoint in production, but instead we have our own implementation below.
func AttachExternalAPIRoutes(r *mux.Router, state *state.State) {
	const basePath = "/v0/"
	r = r.NewRoute().PathPrefix(basePath).Subrouter()

	r.Handle("/tasks/execute", handlers.HandlerWithBody(state, ExecuteTaskHandler)).Methods("POST", "OPTIONS")
	r.Handle("/tasks/getMetadata", handlers.Handler(state, GetTaskMetadataHandler)).Methods("GET", "OPTIONS")
	r.Handle("/tasks/get", handlers.Handler(state, GetTaskInfoHandler)).Methods("GET", "OPTIONS")

	r.Handle("/runs/getOutputs", handlers.Handler(state, GetOutputsHandler)).Methods("GET", "OPTIONS")
	r.Handle("/runs/get", handlers.Handler(state, GetRunHandler)).Methods("GET", "OPTIONS")
	r.Handle("/runs/list", handlers.Handler(state, ListRunsHandler)).Methods("GET", "OPTIONS")

	r.Handle("/resources/list", handlers.Handler(state, ListResourcesHandler)).Methods("GET", "OPTIONS")

	r.Handle("/views/get", handlers.Handler(state, GetViewHandler)).Methods("GET", "OPTIONS")

	r.Handle("/displays/list", handlers.Handler(state, ListDisplaysHandler)).Methods("GET", "OPTIONS")
	r.Handle("/displays/create", handlers.HandlerWithBody(state, CreateDisplayHandler)).Methods("POST", "OPTIONS")

	r.Handle("/prompts/get", handlers.Handler(state, GetPromptHandler)).Methods("GET", "OPTIONS")
	r.Handle("/prompts/create", handlers.HandlerWithBody(state, CreatePromptHandler)).Methods("POST", "OPTIONS")
}

type ExecuteTaskRequest struct {
	RunID       string            `json:"runID"`
	Slug        string            `json:"slug"`
	ParamValues api.Values        `json:"paramValues"`
	Resources   map[string]string `json:"resources"`
}

func getRunIDFromToken(r *http.Request) (string, error) {
	if token := r.Header.Get("X-Airplane-Token"); token != "" {
		claims, err := dev.ParseInsecureAirplaneToken(token)
		if err != nil {
			return "", err
		}
		return claims.RunID, nil
	}
	return "", nil
}

// ExecuteTaskHandler handles requests to the /v0/tasks/execute endpoint
func ExecuteTaskHandler(ctx context.Context, state *state.State, r *http.Request, req ExecuteTaskRequest) (dev.LocalRun, error) {
	// Allow run IDs to be generated beforehand; this is needed so that the /dev/logs endpoint can start waiting
	// for logs for a given run before that run's execution has started.
	runID := req.RunID
	var run dev.LocalRun
	if runID != "" {
		run, _ = state.Runs.Get(runID)
	} else {
		runID = dev.GenerateRunID()
		run = *dev.NewLocalRun()
	}

	parentID, err := getRunIDFromToken(r)
	if err != nil {
		return run, err
	}
	run.ParentID = parentID

	localTaskConfig, ok := state.TaskConfigs[req.Slug]
	isBuiltin := builtins.IsBuiltinTaskSlug(req.Slug)
	parameters := libapi.Parameters{}
	start := time.Now()
	if isBuiltin || ok {
		runConfig := dev.LocalRunConfig{
			ID:          runID,
			ParamValues: req.ParamValues,
			Port:        state.Port,
			Root:        state.CliConfig,
			Slug:        req.Slug,
			EnvSlug:     state.EnvSlug,
			IsBuiltin:   isBuiltin,
			LogBroker:   run.LogBroker,
		}
		resourceAttachments := map[string]string{}
		// Builtins have a specific alias in the form of "rest", "db", etc. that is required by the builtins binary,
		// and so we need to manually generate resource attachments.
		if isBuiltin {
			// The SDK should provide us with exactly one resource for builtins.
			if len(req.Resources) != 1 {
				return dev.LocalRun{}, errors.Errorf("unable to determine resource required by builtin, there is not exactly one resource in request: %+v", req.Resources)
			}

			// Get the only entry in the request resource map.
			var builtinAlias, resourceID string
			for builtinAlias, resourceID = range req.Resources {
			}

			var foundResource bool
			for slug, res := range state.DevConfig.Resources {
				if res.ID() == resourceID {
					resourceAttachments[builtinAlias] = slug
					foundResource = true
				}
			}

			if !foundResource {
				return dev.LocalRun{}, errors.Errorf("resource with id %s not found in dev config file", resourceID)
			}
			run.IsStdAPI = true
			stdapiReq, err := dev.BuiltinRequest(req.Slug, req.ParamValues)
			if err != nil {
				return run, err
			}
			run.StdAPIRequest = stdapiReq
			run.TaskName = req.Slug
		} else if localTaskConfig.Def != nil {
			kind, kindOptions, err := dev.GetKindAndOptions(localTaskConfig)
			if err != nil {
				return dev.LocalRun{}, err
			}
			runConfig.Kind = kind
			runConfig.KindOptions = kindOptions
			runConfig.Name = localTaskConfig.Def.GetName()
			runConfig.File = localTaskConfig.TaskEntrypoint
			resourceAttachments = localTaskConfig.Def.GetResourceAttachments()
			parameters = localTaskConfig.Def.GetParameters()
			run.TaskID = req.Slug
			run.TaskName = localTaskConfig.Def.GetName()

			envVars, err := dev.MaterializeEnvVars(localTaskConfig, state.DevConfig)
			if err != nil {
				return dev.LocalRun{}, err
			}
			runConfig.Env = envVars
		}
		resources, err := resource.GenerateAliasToResourceMap(
			resourceAttachments,
			state.DevConfig.Resources,
		)
		if err != nil {
			return dev.LocalRun{}, errors.Wrap(err, "generating alias to resource map")
		}
		runConfig.Resources = resources
		run.Resources = resource.GenerateResourceAliasToID(resources)
		run.RunID = runID
		run.CreatedAt = start
		run.ParamValues = req.ParamValues
		run.Parameters = &parameters
		run.Status = api.RunActive
		// if the user is authenticated in CLI, use their ID
		run.CreatorID = state.CliConfig.ParseTokenForAnalytics().UserID
		state.Runs.Add(req.Slug, runID, run)

		// use a new context while executing
		// so the handler context doesn't cancel task execution
		outputs, err := state.Executor.Execute(context.Background(), runConfig)

		completedAt := time.Now()
		run, err = state.Runs.Update(runID, func(run *dev.LocalRun) error {
			if err != nil {
				run.Status = api.RunFailed
				run.FailedAt = &completedAt
			} else {
				run.Status = api.RunSucceeded
				run.SucceededAt = &completedAt
			}
			run.Outputs = outputs
			return nil
		})
	} else {
		logger.Error("task with slug %s is not registered locally", req.Slug)
	}

	return run, nil
}

type GetRunResponse struct {
	Run  dev.LocalRun `json:"run"`
	Task *libapi.Task `json:"task"`
}

// GetRunHandler handles requests to the /v0/runs/get endpoint
func GetRunHandler(ctx context.Context, state *state.State, r *http.Request) (dev.LocalRun, error) {
	runID := r.URL.Query().Get("id")
	run, ok := state.Runs.Get(runID)
	if !ok {
		return dev.LocalRun{}, errorlib.HttpError{Msg: "no run with ID found", Code: http.StatusNotFound}
	}

	return run, nil
}

// GetTaskMetadataHandler handles requests to the /v0/tasks/metadata endpoint. It generates a deterministic task ID for
// each task found locally, and its primary purpose is to ensure that the task discoverer does not error.
func GetTaskMetadataHandler(ctx context.Context, state *state.State, r *http.Request) (libapi.TaskMetadata, error) {
	slug := r.URL.Query().Get("slug")
	return libapi.TaskMetadata{
		ID:   fmt.Sprintf("tsk-%s", slug),
		Slug: slug,
	}, nil
}

// GetViewHandler handles requests to the /v0/views/get endpoint. It generates a deterministic view ID for each view
// found locally, and its primary purpose is to ensure that the view discoverer does not error.
func GetViewHandler(ctx context.Context, state *state.State, r *http.Request) (libapi.View, error) {
	slug := r.URL.Query().Get("slug")
	if slug == "" {
		return libapi.View{}, errors.New("slug cannot be empty")
	}

	return libapi.View{
		ID:   fmt.Sprintf("vew-%s", slug),
		Slug: r.URL.Query().Get("slug"),
	}, nil
}

type ListRunsResponse struct {
	Runs []dev.LocalRun `json:"runs"`
}

func ListRunsHandler(ctx context.Context, state *state.State, r *http.Request) (ListRunsResponse, error) {
	taskSlug := r.URL.Query().Get("taskSlug")
	runs := state.Runs.GetRunHistory(taskSlug)
	return ListRunsResponse{
		Runs: runs,
	}, nil
}

type GetOutputsResponse struct {
	// Outputs from this run.
	Output api.Outputs `json:"output"`
}

// GetOutputsHandler handles requests to the /v0/runs/getOutputs endpoint
func GetOutputsHandler(ctx context.Context, state *state.State, r *http.Request) (GetOutputsResponse, error) {
	runID := r.URL.Query().Get("id")
	run, ok := state.Runs.Get(runID)
	if !ok {
		return GetOutputsResponse{}, errors.Errorf("run with id %s not found", runID)
	}

	return GetOutputsResponse{
		Output: run.Outputs,
	}, nil
}

// GetTaskInfoHandler handles requests to the /v0/tasks?slug=<task_slug> endpoint.
func GetTaskInfoHandler(ctx context.Context, state *state.State, r *http.Request) (libapi.UpdateTaskRequest, error) {
	taskSlug := r.URL.Query().Get("slug")
	if taskSlug == "" {
		return libapi.UpdateTaskRequest{}, errors.New("Task slug was not supplied, request path must be of the form /v0/tasks?slug=<task_slug>")
	}
	taskConfig, ok := state.TaskConfigs[taskSlug]
	if !ok {
		return libapi.UpdateTaskRequest{}, errors.Errorf("Task with slug %s not found", taskSlug)
	}
	req, err := taskConfig.Def.GetUpdateTaskRequest(ctx, state.LocalClient)
	if err != nil {
		logger.Error("Encountered error while getting task info: %v", err)
		return libapi.UpdateTaskRequest{}, errors.Errorf("error getting task %s", taskSlug)
	}
	return req, nil
}

type ListDisplaysResponse struct {
	Displays []libapi.Display `json:"displays"`
}

func ListDisplaysHandler(ctx context.Context, state *state.State, r *http.Request) (ListDisplaysResponse, error) {
	runID := r.URL.Query().Get("runID")
	run, ok := state.Runs.Get(runID)
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

func CreateDisplayHandler(ctx context.Context, state *state.State, r *http.Request, req CreateDisplayRequest) (CreateDisplayResponse, error) {
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
		ID:        utils.GenerateID("dsp"),
		RunID:     runID,
		Kind:      req.Display.Kind,
		CreatedAt: now,
		UpdatedAt: now,
	}
	switch req.Display.Kind {
	case "markdown":
		display.Content = req.Display.Content
	case "table":
		display.Rows = req.Display.Rows
		display.Columns = req.Display.Columns
	case "json":
		display.Value = req.Display.Value
	}

	run, err := state.Runs.Update(runID, func(run *dev.LocalRun) error {
		run.Displays = append(run.Displays, display)
		return nil
	})
	if err != nil {
		return CreateDisplayResponse{}, err
	}

	content := fmt.Sprintf("[kind=%s]\n\n%s", display.Kind, display.Content)
	prefix := "[" + logger.Gray(run.TaskID+" display") + "] "
	print.BoxPrintWithPrefix(content, prefix)

	return CreateDisplayResponse{
		Display: display,
	}, nil
}

type PromptResponse struct {
	ID string `json:"id"`
}

func CreatePromptHandler(ctx context.Context, state *state.State, r *http.Request, req libapi.Prompt) (PromptResponse, error) {
	runID, err := getRunIDFromToken(r)
	if err != nil {
		return PromptResponse{}, err
	}
	if runID == "" {
		return PromptResponse{}, errors.Errorf("expected runID from airplane token: %s", err.Error())
	}

	if req.Values == nil {
		req.Values = map[string]interface{}{}
	}

	prompt := libapi.Prompt{
		ID:        utils.GenerateID("pmt"),
		RunID:     runID,
		Schema:    req.Schema,
		Values:    req.Values,
		CreatedAt: time.Now(),
	}

	if _, err := state.Runs.Update(runID, func(run *dev.LocalRun) error {
		run.Prompts = append(run.Prompts, prompt)
		return nil
	}); err != nil {
		return PromptResponse{}, err
	}

	return PromptResponse{ID: prompt.ID}, nil
}

type GetPromptResponse struct {
	Prompt libapi.Prompt `json:"prompt"`
}

func GetPromptHandler(ctx context.Context, state *state.State, r *http.Request) (GetPromptResponse, error) {
	promptID := r.URL.Query().Get("id")
	if promptID == "" {
		return GetPromptResponse{}, errors.New("id is required")
	}
	runID, err := getRunIDFromToken(r)
	if err != nil {
		return GetPromptResponse{}, err
	}
	if runID == "" {
		return GetPromptResponse{}, errors.Errorf("expected runID from airplane token")
	}

	run, ok := state.Runs.Get(runID)
	if !ok {
		return GetPromptResponse{}, errors.New("run not found")
	}

	for _, p := range run.Prompts {
		if p.ID == promptID {
			return GetPromptResponse{Prompt: p}, nil
		}
	}
	return GetPromptResponse{}, errors.New("prompt not found")
}

// ListResourcesHandler handles requests to the /i/resources/list endpoint
func ListResourcesHandler(ctx context.Context, state *state.State, r *http.Request) (libapi.ListResourcesResponse, error) {
	resources := make([]libapi.Resource, 0, len(state.DevConfig.RawResources))
	for slug, res := range state.DevConfig.Resources {
		internalResource, err := conversion.ConvertToInternalResource(res)
		if err != nil {
			return libapi.ListResourcesResponse{}, errors.Wrap(err, "converting to internal resource")
		}
		kindConfig, err := resource.KindConfigToMap(internalResource)
		if err != nil {
			return libapi.ListResourcesResponse{}, err
		}
		resources = append(resources, libapi.Resource{
			ID:                res.ID(),
			Slug:              slug,
			Kind:              libapi.ResourceKind(res.Kind()),
			KindConfig:        kindConfig,
			CanUseResource:    true,
			CanUpdateResource: true,
		})
	}

	return libapi.ListResourcesResponse{
		Resources: resources,
	}, nil
}
