package apiint

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/airplanedev/cli/pkg/dev"
	"github.com/airplanedev/cli/pkg/logger"
	res "github.com/airplanedev/cli/pkg/resource"
	"github.com/airplanedev/cli/pkg/server/errorlib"
	"github.com/airplanedev/cli/pkg/server/handlers"
	"github.com/airplanedev/cli/pkg/server/state"
	"github.com/airplanedev/cli/pkg/utils"
	libapi "github.com/airplanedev/lib/pkg/api"
	"github.com/airplanedev/lib/pkg/resources"
	"github.com/airplanedev/lib/pkg/resources/conversion"
	"github.com/airplanedev/lib/pkg/resources/kind_configs"
	"github.com/gorilla/mux"
	"github.com/gosimple/slug"
	"github.com/pkg/errors"
)

// AttachInternalAPIRoutes attaches a minimal subset of the internal Airplane API endpoints that are necessary for the
// previewer
func AttachInternalAPIRoutes(r *mux.Router, state *state.State) {
	const basePath = "/i/"
	r = r.NewRoute().PathPrefix(basePath).Subrouter()

	r.Handle("/resources/create", handlers.HandlerWithBody(state, CreateResourceHandler)).Methods("POST", "OPTIONS")
	r.Handle("/resources/get", handlers.Handler(state, GetResourceHandler)).Methods("GET", "OPTIONS")
	r.Handle("/resources/list", handlers.Handler(state, ListResourcesHandler)).Methods("GET", "OPTIONS")
	r.Handle("/resources/update", handlers.HandlerWithBody(state, UpdateResourceHandler)).Methods("POST", "OPTIONS")
	r.Handle("/resources/delete", handlers.HandlerWithBody(state, DeleteResourceHandler)).Methods("POST", "OPTIONS")
	r.Handle("/resources/isSlugAvailable", handlers.Handler(state, IsResourceSlugAvailableHandler)).Methods("GET", "OPTIONS")

	r.Handle("/prompts/list", handlers.Handler(state, ListPromptHandler)).Methods("GET", "OPTIONS")
	r.Handle("/prompts/submit", handlers.HandlerWithBody(state, SubmitPromptHandler)).Methods("POST", "OPTIONS")

	r.Handle("/runs/get", handlers.Handler(state, GetRunHandler)).Methods("GET", "OPTIONS")
	r.Handle("/runs/getDescendants", handlers.Handler(state, GetDescendantsHandler)).Methods("GET", "OPTIONS")

	r.Handle("/users/get", handlers.Handler(state, GetUserHandler)).Methods("GET", "OPTIONS")
}

type CreateResourceRequest struct {
	Name           string                          `json:"name"`
	Slug           string                          `json:"slug"`
	Kind           resources.ResourceKind          `json:"kind"`
	KindConfig     kind_configs.ResourceKindConfig `json:"kindConfig"`
	ExportResource resources.Resource              `json:"resource"`
}

func (r *CreateResourceRequest) UnmarshalJSON(buf []byte) error {
	var raw struct {
		Name           string                          `json:"name"`
		Slug           string                          `json:"slug"`
		Kind           resources.ResourceKind          `json:"kind"`
		KindConfig     kind_configs.ResourceKindConfig `json:"kindConfig"`
		ExportResource map[string]interface{}          `json:"resource"`
	}
	if err := json.Unmarshal(buf, &raw); err != nil {
		return err
	}

	var export resources.Resource
	var err error
	if raw.ExportResource != nil {
		export, err = resources.GetResource(resources.ResourceKind(raw.Kind), raw.ExportResource)
		if err != nil {
			return err
		}
	}

	r.Name = raw.Name
	r.Slug = raw.Slug
	r.Kind = raw.Kind
	r.KindConfig = raw.KindConfig
	r.ExportResource = export

	return nil
}

type CreateResourceResponse struct {
	ResourceID string `json:"resourceID"`
}

// CreateResourceHandler handles requests to the /i/resources/get endpoint
func CreateResourceHandler(ctx context.Context, state *state.State, r *http.Request, req CreateResourceRequest) (CreateResourceResponse, error) {
	resourceSlug := req.Slug
	var err error
	if resourceSlug == "" {
		if resourceSlug, err = utils.GetUniqueSlug(utils.GetUniqueSlugRequest{
			Slug: slug.Make(req.Name),
			SlugExists: func(slug string) (bool, error) {
				_, ok := state.DevConfig.Resources[slug]
				return ok, nil
			},
		}); err != nil {
			return CreateResourceResponse{}, errors.New("Could not generate unique resource slug")
		}
	} else {
		if _, ok := state.DevConfig.Resources[resourceSlug]; ok {
			return CreateResourceResponse{}, errors.Errorf("Resource with slug %s already exists", resourceSlug)
		}
	}

	id := fmt.Sprintf("res-%s", resourceSlug)
	var resource resources.Resource
	if req.ExportResource != nil {
		resource = req.ExportResource
		if err := resource.UpdateBaseResource(resources.BaseResource{
			ID:   id,
			Slug: resourceSlug,
			Kind: req.Kind,
			Name: req.Name,
		}); err != nil {
			return CreateResourceResponse{}, errors.Wrap(err, "updating base resoruce")
		}
	} else {
		internalResource := kind_configs.InternalResource{
			ID:             id,
			Slug:           resourceSlug,
			Kind:           req.Kind,
			Name:           req.Name,
			KindConfig:     req.KindConfig,
			ExportResource: req.ExportResource,
		}

		resource, err = internalResource.ToExternalResource()
		if err != nil {
			return CreateResourceResponse{}, errors.Wrap(err, "converting to external resource")
		}
	}

	if err := state.DevConfig.SetResource(resourceSlug, resource); err != nil {
		return CreateResourceResponse{}, errors.Wrap(err, "setting resource")
	}

	return CreateResourceResponse{ResourceID: id}, nil
}

type GetResourceResponse struct {
	Resource kind_configs.InternalResource
}

// GetResourceHandler handles requests to the /i/resources/get endpoint
func GetResourceHandler(ctx context.Context, state *state.State, r *http.Request) (GetResourceResponse, error) {
	slug := r.URL.Query().Get("slug")
	if slug == "" {
		return GetResourceResponse{}, errors.Errorf("Resource slug was not supplied")
	}

	for s, resource := range state.DevConfig.Resources {
		if s == slug {
			internalResource, err := conversion.ConvertToInternalResource(resource)
			if err != nil {
				return GetResourceResponse{}, errors.Wrap(err, "converting to internal resource")
			}
			return GetResourceResponse{Resource: internalResource}, nil
		}
	}

	return GetResourceResponse{}, errors.Errorf("Resource with slug %s is not in dev config file", slug)
}

// ListResourcesHandler handles requests to the /i/resources/list endpoint
func ListResourcesHandler(ctx context.Context, state *state.State, r *http.Request) (libapi.ListResourcesResponse, error) {
	resources := make([]libapi.Resource, 0, len(state.DevConfig.RawResources))
	for slug, resource := range state.DevConfig.Resources {
		internalResource, err := conversion.ConvertToInternalResource(resource)
		if err != nil {
			return libapi.ListResourcesResponse{}, errors.Wrap(err, "converting to internal resource")
		}
		kindConfig, err := res.KindConfigToMap(internalResource)
		if err != nil {
			return libapi.ListResourcesResponse{}, err
		}
		resources = append(resources, libapi.Resource{
			ID:                resource.ID(),
			Name:              slug, // TODO: Change to actual name of resource once that's exposed from export resource.
			Slug:              slug,
			Kind:              libapi.ResourceKind(resource.Kind()),
			KindConfig:        kindConfig,
			ExportResource:    resource,
			CanUseResource:    true,
			CanUpdateResource: true,
		})
	}

	return libapi.ListResourcesResponse{
		Resources: resources,
	}, nil
}

type UpdateResourceRequest struct {
	ID             string                          `json:"id"`
	Slug           string                          `json:"slug"`
	Name           string                          `json:"name"`
	Kind           resources.ResourceKind          `json:"kind"`
	KindConfig     kind_configs.ResourceKindConfig `json:"kindConfig"`
	ExportResource resources.Resource              `json:"resource"`
}

func (r *UpdateResourceRequest) UnmarshalJSON(buf []byte) error {
	var raw struct {
		ID             string                          `json:"id"`
		Slug           string                          `json:"slug"`
		Name           string                          `json:"name"`
		Kind           resources.ResourceKind          `json:"kind"`
		KindConfig     kind_configs.ResourceKindConfig `json:"kindConfig"`
		ExportResource map[string]interface{}          `json:"resource"`
	}
	if err := json.Unmarshal(buf, &raw); err != nil {
		return err
	}

	var export resources.Resource
	var err error
	if raw.ExportResource != nil {
		export, err = resources.GetResource(resources.ResourceKind(raw.Kind), raw.ExportResource)
		if err != nil {
			return err
		}
	}

	r.ID = raw.ID
	r.Slug = raw.Slug
	r.Name = raw.Name
	r.Kind = raw.Kind
	r.KindConfig = raw.KindConfig
	r.ExportResource = export

	return nil
}

type UpdateResourceResponse struct {
	ResourceID string `json:"resourceID"`
}

// UpdateResourceHandler handles requests to the /i/resources/get endpoint
func UpdateResourceHandler(ctx context.Context, state *state.State, r *http.Request, req UpdateResourceRequest) (UpdateResourceResponse, error) {
	// Check if resource exists in dev config file.
	var foundResource bool
	var oldSlug string
	var resource resources.Resource
	for configSlug, configResource := range state.DevConfig.Resources {
		// We can't rely on the slug for existence since it may have changed.
		if configResource.ID() == req.ID {
			foundResource = true
			resource = configResource
			oldSlug = configSlug
			break
		}
	}
	if !foundResource {
		return UpdateResourceResponse{}, errors.Errorf("resource with slug %s not found in dev config file", req.Slug)
	}

	// Set a new resource ID based on the new slug.
	newResourceID := fmt.Sprintf("res-%s", req.Slug)

	var newResource resources.Resource
	if req.ExportResource != nil {
		if err := resource.Update(req.ExportResource); err != nil {
			return UpdateResourceResponse{}, errors.Wrap(err, "updating resource")
		}
		if err := resource.UpdateBaseResource(resources.BaseResource{
			ID:   newResourceID,
			Slug: req.Slug,
			Name: req.Name,
		}); err != nil {
			return UpdateResourceResponse{}, errors.Wrap(err, "updating base resoruce")
		}
		newResource = resource
	} else {
		// Convert to internal representation of resource for updating.
		internalResource, err := conversion.ConvertToInternalResource(resource)
		if err != nil {
			return UpdateResourceResponse{}, errors.Wrap(err, "converting to external resource")
		}

		// Update internal resource - utilize KindConfig.Update to not overwrite sensitive fields.
		internalResource.Slug = req.Slug
		internalResource.Name = req.Name
		internalResource.ID = newResourceID
		if err := internalResource.KindConfig.Update(req.KindConfig); err != nil {
			return UpdateResourceResponse{}, errors.Wrap(err, "updating kind config of internal resource")
		}

		// Convert back to external representation of resource.
		newResource, err = internalResource.ToExternalResource()
		if err != nil {
			return UpdateResourceResponse{}, errors.Wrap(err, "converting to external resource")
		}
	}

	// Remove the old resource first - we need to do this since DevConfig.Resources is a mapping from slug to resource,
	// and if the update resource request involves updating the slug, we don't want to leave the old resource (under the
	// old slug) in the dev config file.
	if err := state.DevConfig.RemoveResource(oldSlug); err != nil {
		return UpdateResourceResponse{}, errors.Wrap(err, "deleting old resource")
	}

	if err := state.DevConfig.SetResource(req.Slug, newResource); err != nil {
		return UpdateResourceResponse{}, errors.Wrap(err, "setting resource")
	}

	return UpdateResourceResponse{
		ResourceID: newResourceID,
	}, nil
}

type DeleteResourceRequest struct {
	ID string `json:"id"`
}

// DeleteResourceHandler handles requests to the /i/resources/delete endpoint
// The web app does utilize the response of the resource deletion handler.
func DeleteResourceHandler(ctx context.Context, state *state.State, r *http.Request, req DeleteResourceRequest) (struct{}, error) {
	id := req.ID
	resourceSlug, err := res.ResourceSlugFromID(id)
	if err != nil {
		return struct{}{}, err
	}

	if _, ok := state.DevConfig.Resources[resourceSlug]; !ok {
		return struct{}{}, errors.Errorf("resource with id %s does not exist in dev config file", id)
	}

	if err := state.DevConfig.RemoveResource(resourceSlug); err != nil {
		return struct{}{}, errors.Wrap(err, "setting resource")
	}

	return struct{}{}, nil
}

type IsResourceSlugAvailableResponse struct {
	Available bool `json:"available"`
}

// IsResourceSlugAvailableHandler handles requests to the /i/resources/isSlugAvailable endpoint
func IsResourceSlugAvailableHandler(ctx context.Context, state *state.State, r *http.Request) (IsResourceSlugAvailableResponse, error) {
	slug := r.URL.Query().Get("slug")
	res, ok := state.DevConfig.Resources[slug]
	return IsResourceSlugAvailableResponse{
		Available: !ok || res.ID() == r.URL.Query().Get("id"),
	}, nil
}

type ListPromptsResponse struct {
	Prompts []libapi.Prompt `json:"prompts"`
}

func ListPromptHandler(ctx context.Context, state *state.State, r *http.Request) (ListPromptsResponse, error) {
	runID := r.URL.Query().Get("runID")
	if runID == "" {
		return ListPromptsResponse{}, errorlib.HttpError{Code: http.StatusBadRequest, Msg: "run ID is required"}
	}

	run, ok := state.Runs.Get(runID)
	if !ok {
		return ListPromptsResponse{}, errorlib.HttpError{Code: http.StatusNotFound, Msg: "run not found"}
	}

	return ListPromptsResponse{Prompts: run.Prompts}, nil
}

type SubmitPromptRequest struct {
	ID          string                 `json:"id"`
	Values      map[string]interface{} `json:"values"`
	RunID       string                 `json:"runID"`
	SubmittedBy *string                `json:"-"`
}

type PromptResponse struct {
	ID string `json:"id"`
}

func SubmitPromptHandler(ctx context.Context, state *state.State, r *http.Request, req SubmitPromptRequest) (PromptResponse, error) {
	if req.ID == "" {
		return PromptResponse{}, errorlib.HttpError{Code: http.StatusBadRequest, Msg: "prompt ID is required"}
	}
	if req.RunID == "" {
		return PromptResponse{}, errorlib.HttpError{Code: http.StatusBadRequest, Msg: "run ID is required"}
	}

	userID := state.CliConfig.ParseTokenForAnalytics().UserID

	_, err := state.Runs.Update(req.RunID, func(run *dev.LocalRun) error {
		for i := range run.Prompts {
			if run.Prompts[i].ID == req.ID {
				now := time.Now()
				run.Prompts[i].SubmittedAt = &now
				run.Prompts[i].Values = req.Values
				run.Prompts[i].SubmittedBy = &userID
				return nil
			}
		}
		return errorlib.HttpError{Code: http.StatusNotFound, Msg: "prompt does not exist"}
	})
	if err != nil {
		return PromptResponse{}, err
	}
	return PromptResponse{ID: req.ID}, nil
}

type GetDescendantsResponse struct {
	Descendants []dev.LocalRun `json:"descendants"`
}

func GetDescendantsHandler(ctx context.Context, state *state.State, r *http.Request) (GetDescendantsResponse, error) {
	runID := r.URL.Query().Get("runID")
	if runID == "" {
		return GetDescendantsResponse{}, errorlib.HttpError{Code: http.StatusBadRequest, Msg: "runID cannot be empty"}
	}
	descendants := state.Runs.GetDescendants(runID)

	return GetDescendantsResponse{
		Descendants: descendants,
	}, nil
}

type User struct {
	ID        string  `json:"userID" db:"id"`
	Email     string  `json:"email" db:"email"`
	Name      string  `json:"name" db:"name"`
	AvatarURL *string `json:"avatarURL" db:"avatar_url"`
}

type GetUserResponse struct {
	User User `json:"user"`
}

func GetUserHandler(ctx context.Context, state *state.State, r *http.Request) (GetUserResponse, error) {
	userID := r.URL.Query().Get("userID")
	// Set avatar to anonymous silhouette
	gravatarURL := "https://www.gravatar.com/avatar?d=mp"
	return GetUserResponse{
		User: User{
			ID:        userID,
			Email:     "hello@airplane.dev",
			Name:      "Airplane editor",
			AvatarURL: &gravatarURL,
		},
	}, nil
}

type GetRunResponse struct {
	Run  dev.LocalRun `json:"run"`
	Task *libapi.Task `json:"task"`
}

func GetRunHandler(ctx context.Context, state *state.State, r *http.Request) (GetRunResponse, error) {
	runID := r.URL.Query().Get("id")
	run, ok := state.Runs.Get(runID)
	if !ok {
		return GetRunResponse{}, errorlib.HttpError{
			Code: http.StatusNotFound,
			Msg:  fmt.Sprintf("no run with ID found: %s", runID),
		}
	}
	response := GetRunResponse{Run: run}

	if taskConfig, ok := state.TaskConfigs[run.TaskID]; ok {
		utr, err := taskConfig.Def.GetUpdateTaskRequest(ctx, state.LocalClient)
		if err != nil {
			logger.Error("Encountered error while getting task info: %v", err)
			return GetRunResponse{}, errorlib.HttpError{
				Code: http.StatusNotFound,
				Msg:  fmt.Sprintf("error getting task %s", taskConfig.Def.GetSlug()),
			}
		}
		response.Task = &libapi.Task{
			ID:          taskConfig.Def.GetSlug(),
			Name:        taskConfig.Def.GetName(),
			Slug:        taskConfig.Def.GetSlug(),
			Description: taskConfig.Def.GetDescription(),
			Parameters:  utr.Parameters,
		}
	}

	return response, nil
}
