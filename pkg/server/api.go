package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/dev"
	"github.com/airplanedev/cli/pkg/logger"
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
	CreatorID   string                 `json:"creatorId"`
	SucceededAt *time.Time             `json:"succeededAt"`
	FailedAt    *time.Time             `json:"failedAt"`
	ParamValues map[string]interface{} `json:"paramValues"`
	Parameters  *libapi.Parameters     `json:"parameters"`
	LogConfig   dev.LogConfig          `json:"-"`
}

// NewLocalRun initializes a run for local dev.
func NewLocalRun() *LocalRun {
	return &LocalRun{
		Status: api.RunQueued,
		LogConfig: dev.LogConfig{
			Channel: make(chan string),
			Logs:    make([]string, 0),
		},
	}
}

// AttachAPIRoutes attaches a minimal subset of the actual Airplane API endpoints that are necessary to locally develop
// a task. For example, a workflow task might call airplane.execute, which would normally make a request to the
// /v0/tasks/execute endpoint in production, but instead we have our own implementation below.
func AttachAPIRoutes(r *mux.Router, state *State) {
	const basePath = "/v0/"
	r = r.NewRoute().PathPrefix(basePath).Subrouter()

	r.Handle("/tasks/execute", ExecuteTaskHandler(state)).Methods("POST", "OPTIONS")
	r.Handle("/tasks/getMetadata", GetTaskMetadataHandler(state)).Methods("GET", "OPTIONS")
	r.Handle("/tasks/get", GetTaskInfoHandler(state)).Methods("GET", "OPTIONS")
	r.Handle("/runs/getOutputs", GetOutputsHandler(state)).Methods("GET", "OPTIONS")
	r.Handle("/runs/get", GetRunHandler(state)).Methods("GET", "OPTIONS")
	r.Handle("/runs/list", ListRunsHandler(state)).Methods("GET", "OPTIONS")

	r.Handle("/resources/list", ListResourcesHandler(state)).Methods("GET", "OPTIONS")

	r.Handle("/views/get", GetViewHandler(state)).Methods("GET", "OPTIONS")
}

type ExecuteTaskRequest struct {
	RunID       string            `json:"runID"`
	Slug        string            `json:"slug"`
	ParamValues api.Values        `json:"paramValues"`
	Resources   map[string]string `json:"resources"`
}

// ExecuteTaskHandler handles requests to the /v0/tasks/execute endpoint
func ExecuteTaskHandler(state *State) http.HandlerFunc {
	return Wrap(func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		var req ExecuteTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			return errors.Wrap(err, "failed to decode request body")
		}

		// Allow run IDs to be generated beforehand; this is needed so that the /dev/logs endpoint can start waiting
		// for logs for a given run before that run's execution has started.
		runID := req.RunID
		var run LocalRun
		if runID != "" {
			run, _ = state.runs.get(runID)
		} else {
			runID = "run" + utils.RandomString(10, utils.CharsetLowercaseNumeric)
			run = *NewLocalRun()
		}

		localTaskConfig, ok := state.taskConfigs[req.Slug]
		isBuiltin := builtins.IsBuiltinTaskSlug(req.Slug)
		var parameters libapi.Parameters
		start := time.Now()
		if isBuiltin || ok {
			runConfig := dev.LocalRunConfig{
				ParamValues: req.ParamValues,
				Port:        state.port,
				Root:        state.cliConfig,
				Slug:        req.Slug,
				EnvSlug:     state.envSlug,
				IsBuiltin:   isBuiltin,
				LogConfig:   run.LogConfig,
			}
			resourceAttachments := map[string]string{}
			if isBuiltin {
				// builtins have access to all resources the parent task has
				for slug := range state.devConfig.DecodedResources {
					resourceAttachments[slug] = slug
				}
			} else if localTaskConfig.Def != nil {
				kind, kindOptions, err := localTaskConfig.Def.GetKindAndOptions()
				if err != nil {
					return errors.Wrap(err, "failed to get kind and options from task config")
				}
				runConfig.Kind = kind
				runConfig.KindOptions = kindOptions
				runConfig.Name = localTaskConfig.Def.GetName()
				runConfig.File = localTaskConfig.Def.GetDefnFilePath()
				resourceAttachments = localTaskConfig.Def.GetResourceAttachments()
				parameters = localTaskConfig.Def.GetParameters()

			}
			resources, err := resource.GenerateAliasToResourceMap(
				resourceAttachments,
				state.devConfig.DecodedResources,
			)
			if err != nil {
				return errors.Wrap(err, "generating alias to resource map")
			}
			runConfig.Resources = resources

			outputs, err := state.executor.Execute(ctx, runConfig)
			if err != nil {
				run.Status = api.RunFailed
			} else if err == nil {
				run.Status = api.RunSucceeded
			}
			run.Outputs = outputs
		} else {
			logger.Error("task with slug %s is not registered locally", req.Slug)
		}

		run.RunID = runID
		run.CreatedAt = start
		run.ParamValues = req.ParamValues
		run.Parameters = &parameters
		now := time.Now()
		if run.Status == api.RunSucceeded {
			run.SucceededAt = &now
		} else {
			run.FailedAt = &now
		}

		state.runs.add(req.Slug, runID, run)

		return json.NewEncoder(w).Encode(LocalRun{
			RunID:       runID,
			Outputs:     run.Outputs,
			Status:      run.Status,
			CreatedAt:   run.CreatedAt,
			CreatorID:   run.CreatorID,
			SucceededAt: run.SucceededAt,
			FailedAt:    run.FailedAt,
			ParamValues: run.ParamValues,
			Parameters:  run.Parameters,
		})
	})
}

// GetTaskMetadataHandler handles requests to the /v0/tasks/metadata endpoint. It generates a deterministic task ID for
// each task found locally, and its primary purpose is to ensure that the task discoverer does not error.
func GetTaskMetadataHandler(state *State) http.HandlerFunc {
	return Wrap(func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		slug := r.URL.Query().Get("slug")
		return json.NewEncoder(w).Encode(libapi.TaskMetadata{
			ID:   fmt.Sprintf("tsk-%s", slug),
			Slug: slug,
		})
	})
}

// GetViewHandler handles requests to the /v0/views/get endpoint. It generates a deterministic view ID for each view
// found locally, and its primary purpose is to ensure that the view discoverer does not error.
func GetViewHandler(state *State) http.HandlerFunc {
	return Wrap(func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		slug := r.URL.Query().Get("slug")
		if slug == "" {
			return errors.New("slug cannot be empty")
		}

		return json.NewEncoder(w).Encode(libapi.View{
			ID:   fmt.Sprintf("vew-%s", slug),
			Slug: r.URL.Query().Get("slug"),
		})
	})
}

// GetRunHandler handles requests to the /v0/runs/get endpoint
func GetRunHandler(state *State) http.HandlerFunc {
	return Wrap(func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		runID := r.URL.Query().Get("id")
		run, ok := state.runs.get(runID)
		if !ok {
			return errors.Errorf("run with id %s not found", runID)
		}
		return json.NewEncoder(w).Encode(run)
	})
}

type ListRunsResponse struct {
	Runs []LocalRun `json:"runs"`
}

func ListRunsHandler(state *State) http.HandlerFunc {
	return Wrap(func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		taskSlug := r.URL.Query().Get("taskSlug")
		runs := state.runs.getRunHistory(taskSlug)
		return json.NewEncoder(w).Encode(ListRunsResponse{
			Runs: runs,
		})
	})
}

type GetOutputsResponse struct {
	// Outputs from this run.
	Output api.Outputs `json:"output"`
}

// GetOutputsHandler handles requests to the /v0/runs/getOutputs endpoint
func GetOutputsHandler(state *State) http.HandlerFunc {
	return Wrap(func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		runID := r.URL.Query().Get("id")
		run, ok := state.runs.get(runID)
		if !ok {
			return errors.Errorf("run with id %s not found", runID)
		}

		return json.NewEncoder(w).Encode(GetOutputsResponse{
			Output: run.Outputs,
		})
	})
}

// ListResourcesHandler handles requests to the /v0/resources/list endpoint
func ListResourcesHandler(state *State) http.HandlerFunc {
	return Wrap(func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		resources := make([]libapi.Resource, 0, len(state.devConfig.Resources))
		for slug := range state.devConfig.Resources {
			// It doesn't matter what we include in the resource struct, as long as we include the slug - this handler
			// is only used so that requests to the local dev api server for this endpoint don't error, in particular:
			// https://github.com/airplanedev/lib/blob/d4c8ed7d1b30095c5cacac2b5c4da8f3ada6378f/pkg/deploy/taskdir/definitions/def_0_3.go#L1081-L1087
			resources = append(resources, libapi.Resource{
				Slug: slug,
			})
		}

		return json.NewEncoder(w).Encode(libapi.ListResourcesResponse{
			Resources: resources,
		})
	})
}
