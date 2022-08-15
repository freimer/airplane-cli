package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/utils"
	"github.com/airplanedev/cli/pkg/views"
	"github.com/airplanedev/cli/pkg/views/viewdir"
	"github.com/airplanedev/lib/pkg/deploy/taskdir/definitions"
	"github.com/gorilla/mux"
	"github.com/pkg/errors"
)

// AttachDevRoutes attaches the endpoints necessary to locally develop a task in the Airplane IDE.
func AttachDevRoutes(r *mux.Router, state *State) {
	const basePath = "/dev/"
	r = r.NewRoute().PathPrefix(basePath).Subrouter()

	r.Handle("/ping", PingHandler()).Methods("GET", "OPTIONS")
	r.Handle("/list", Handler(state, ListEntrypointsHandler)).Methods("GET", "OPTIONS")
	r.Handle("/tasks/{task_slug}", Handler(state, GetTaskHandler)).Methods("GET", "OPTIONS")
	r.Handle("/startView/{view_slug}", Handler(state, StartViewHandler)).Methods("POST", "OPTIONS")
	r.Handle("/logs/{run_id}", LogsHandler(state)).Methods("GET", "OPTIONS")
	r.Handle("/runs/create", HandlerWithBody(state, CreateRunHandler)).Methods("POST", "OPTIONS")
}

// PingHandler handles request to the /dev/ping endpoint.
func PingHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}
}

type AppKind string

const (
	AppKindTask = "task"
	AppKindView = "view"
)

// AppMetadata represents metadata for a task or view.
type AppMetadata struct {
	Name string  `json:"name"`
	Slug string  `json:"slug"`
	Kind AppKind `json:"kind"`
}

type ListEntrypointsHandlerResponse struct {
	Entrypoints map[string][]AppMetadata `json:"entrypoints"`
}

// ListEntrypointsHandler handles requests to the /dev/list endpoint. It generates a mapping from entrypoint relative to
// the dev server root to the list of tasks and views that use that entrypoint.
func ListEntrypointsHandler(ctx context.Context, state *State, r *http.Request) (ListEntrypointsHandlerResponse, error) {
	entrypoints := make(map[string][]AppMetadata)
	for slug, taskConfig := range state.taskConfigs {
		absoluteEntrypoint := taskConfig.TaskEntrypoint

		ep, err := filepath.Rel(state.dir, absoluteEntrypoint)
		if err != nil {
			return ListEntrypointsHandlerResponse{}, errors.Wrap(err, "Getting relative path")
		}
		if _, ok := entrypoints[ep]; !ok {
			entrypoints[ep] = make([]AppMetadata, 0, 1)
		}
		entrypoints[ep] = append(entrypoints[ep], AppMetadata{
			Name: taskConfig.Def.GetName(),
			Slug: slug,
			Kind: AppKindTask,
		})
	}

	for slug, viewConfig := range state.viewConfigs {
		absoluteEntrypoint := viewConfig.Def.Entrypoint

		ep, err := filepath.Rel(state.dir, absoluteEntrypoint)
		if err != nil {
			return ListEntrypointsHandlerResponse{}, errors.Wrap(err, "Getting relative path")
		}
		if _, ok := entrypoints[ep]; !ok {
			entrypoints[ep] = make([]AppMetadata, 0, 1)
		}
		entrypoints[ep] = append(entrypoints[ep], AppMetadata{
			Name: viewConfig.Def.Name,
			Slug: slug,
			Kind: AppKindView,
		})
	}

	return ListEntrypointsHandlerResponse{
		Entrypoints: entrypoints,
	}, nil
}

type CreateRunResponse struct {
	RunID string `json:"runID"`
}

type CreateRunRequest struct {
	TaskSlug string `json:"taskSlug"`
}

func CreateRunHandler(ctx context.Context, state *State, req CreateRunRequest) (CreateRunResponse, error) {
	if req.TaskSlug == "" {
		return CreateRunResponse{}, errors.New("Task slug is required")
	}

	runID := "run" + utils.RandomString(10, utils.CharsetLowercaseNumeric)
	run := *NewLocalRun()
	run.CreatorID = state.cliConfig.ParseTokenForAnalytics().UserID
	state.runs.add(req.TaskSlug, runID, run)
	return CreateRunResponse{RunID: runID}, nil
}

// GetTaskHandler handles requests to the /dev/tasks/<task_slug> endpoint.
func GetTaskHandler(ctx context.Context, state *State, r *http.Request) (definitions.DefinitionInterface, error) {
	vars := mux.Vars(r)
	taskSlug, ok := vars["task_slug"]
	if !ok {
		return nil, errors.Errorf("Task slug was not supplied, request path must be of the form /dev/tasks/<task_slug>")
	}

	taskConfig, ok := state.taskConfigs[taskSlug]
	if !ok {
		return nil, errors.Errorf("Task with slug %s not found", taskSlug)
	}

	return taskConfig.Def, nil
}

type StartViewResponse struct {
	ViteServer string `json:"viteServer"`
}

// StartViewHandler handles requests to the /dev/tasks/<task_slug> endpoint.
func StartViewHandler(ctx context.Context, state *State, r *http.Request) (StartViewResponse, error) {
	// TODO: Maintain mapping between view and vite process instead of starting a new vite process on each request.
	state.viteMutex.Lock()
	defer state.viteMutex.Unlock()
	if state.viteProcess != nil {
		if err := state.viteProcess.Kill(); err != nil {
			return StartViewResponse{}, errors.Wrap(err, "killing previous vite process")
		}
	}

	vars := mux.Vars(r)
	viewSlug, ok := vars["view_slug"]
	if !ok {
		return StartViewResponse{}, errors.Errorf("View slug was not supplied, request path must be of the form /dev/startView/<view_slug>")
	}

	viewConfig, ok := state.viewConfigs[viewSlug]
	if !ok {
		return StartViewResponse{}, errors.Errorf("View with slug %s not found", viewSlug)
	}

	rootDir, err := viewdir.FindRoot(viewConfig.Root)
	if err != nil {
		return StartViewResponse{}, err
	}

	vd, err := viewdir.NewViewDirectory(ctx, state.cliConfig, rootDir, viewConfig.Root, state.envSlug)
	if err != nil {
		return StartViewResponse{}, err
	}

	cmd, viteServer, err := views.Dev(vd, views.ViteOpts{
		Client:  state.cliConfig.Client,
		EnvSlug: state.envSlug,
		TTY:     false,
	})
	if err != nil {
		return StartViewResponse{}, errors.Wrap(err, "starting views dev")
	}

	state.viteProcess = cmd.Process

	u, err := url.Parse(viteServer)
	if err != nil {
		return StartViewResponse{}, errors.Wrapf(err, "parsing vite server url %s", viteServer)
	}

	// Wait for Vite to become ready
	for {
		conn, _ := net.DialTimeout("tcp", u.Host, 10*time.Second)
		if conn != nil {
			conn.Close()
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	return StartViewResponse{ViteServer: viteServer}, nil
}

func LogsHandler(state *State) http.HandlerFunc {
	// TODO: Extract logic into SSE Handler
	return Wrap(func(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		vars := mux.Vars(r)
		runID, ok := vars["run_id"]
		if !ok {
			return errors.Errorf("Run id was not supplied, request path must be of the form /dev/logs/<run_id>")
		}

		run, ok := state.runs.get(runID)
		if !ok {
			return errors.Errorf("Run with id %s not found", runID)
		}

		// Send logs that have already been persisted.
		// TODO: Move this to a dedicated /getLogs endpoint
		for _, log := range run.LogStore.Logs {
			encodedLog, err := json.Marshal(log)
			if err != nil {
				return errors.Wrap(err, "marshaling log")
			}
			fmt.Fprintf(w, "data: %s\n\n", encodedLog)
		}

		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		// If the run has finished, then we've already sent all the logs above.
		if run.Status == api.RunSucceeded {
			return nil
		}

		logger.Debug("Waiting for logs from command output")
		for {
			select {
			case log := <-run.LogStore.Channel:
				logger.Debug("Sending log to event stream")
				encodedLog, err := json.Marshal(log)
				if err != nil {
					return errors.Wrap(err, "marshaling log")
				}
				fmt.Fprintf(w, "data: %s\n\n", encodedLog)
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				run.LogStore.Logs = append(run.LogStore.Logs, log)
			// If the client has closed their request, then we should stop the event stream.
			case <-r.Context().Done():
				logger.Debug("Event stream closed")
				return nil
			// If the command has finished running, we should close the event stream.
			case <-run.LogStore.DoneChannel:
				logger.Debug("Done executing")
				return nil
			}
		}
	})
}
