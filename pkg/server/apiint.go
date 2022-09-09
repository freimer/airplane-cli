package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	res "github.com/airplanedev/cli/pkg/resource"
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
func AttachInternalAPIRoutes(r *mux.Router, state *State) {
	const basePath = "/i/"
	r = r.NewRoute().PathPrefix(basePath).Subrouter()

	r.Handle("/resources/create", HandlerWithBody(state, CreateResourceHandler)).Methods("POST", "OPTIONS")
	r.Handle("/resources/get", Handler(state, GetResourceHandler)).Methods("GET", "OPTIONS")
	r.Handle("/resources/list", Handler(state, ListResourcesHandler)).Methods("GET", "OPTIONS")
	r.Handle("/resources/update", HandlerWithBody(state, UpdateResourceHandler)).Methods("POST", "OPTIONS")
	r.Handle("/resources/isSlugAvailable", Handler(state, IsResourceSlugAvailableHandler)).Methods("GET", "OPTIONS")

	r.Handle("/prompts/list", Handler(state, ListPromptHandler)).Methods("GET", "OPTIONS")
	r.Handle("/prompts/submit", HandlerWithBody(state, SubmitPromptHandler)).Methods("POST", "OPTIONS")

	r.Handle("/runs/getDescendants", Handler(state, GetDescendantsHandler)).Methods("GET", "OPTIONS")

	r.Handle("/users/get", Handler(state, GetUserHandler)).Methods("GET", "OPTIONS")
}

type CreateResourceRequest struct {
	Name       string                          `json:"name"`
	Slug       string                          `json:"slug"`
	Kind       resources.ResourceKind          `json:"kind"`
	KindConfig kind_configs.ResourceKindConfig `json:"kindConfig"`
}

type CreateResourceResponse struct {
	ResourceID string `json:"resourceID"`
}

// CreateResourceHandler handles requests to the /i/resources/get endpoint
func CreateResourceHandler(ctx context.Context, state *State, r *http.Request, req CreateResourceRequest) (CreateResourceResponse, error) {
	resourceSlug := req.Slug
	var err error
	if resourceSlug == "" {
		if resourceSlug, err = utils.GetUniqueSlug(utils.GetUniqueSlugRequest{
			Slug: slug.Make(req.Name),
			SlugExists: func(slug string) (bool, error) {
				_, ok := state.devConfig.Resources[slug]
				return ok, nil
			},
		}); err != nil {
			return CreateResourceResponse{}, errors.New("Could not generate unique resource slug")
		}
	} else {
		if _, ok := state.devConfig.Resources[resourceSlug]; ok {
			return CreateResourceResponse{}, errors.Errorf("Resource with slug %s already exists", resourceSlug)
		}
	}

	id := fmt.Sprintf("res-%s", resourceSlug)
	internalResource := kind_configs.InternalResource{
		ID:         id,
		Slug:       resourceSlug,
		Kind:       req.Kind,
		Name:       req.Name,
		KindConfig: req.KindConfig,
	}

	resource, err := internalResource.ToExternalResource()
	if err != nil {
		return CreateResourceResponse{}, errors.Wrap(err, "converting to external resource")
	}

	if err := state.devConfig.SetResource(resourceSlug, resource); err != nil {
		return CreateResourceResponse{}, errors.Wrap(err, "setting resource")
	}

	return CreateResourceResponse{ResourceID: id}, nil
}

type GetResourceResponse struct {
	Resource kind_configs.InternalResource
}

// GetResourceHandler handles requests to the /i/resources/get endpoint
func GetResourceHandler(ctx context.Context, state *State, r *http.Request) (GetResourceResponse, error) {
	slug := r.URL.Query().Get("slug")
	if slug == "" {
		return GetResourceResponse{}, errors.Errorf("Resource slug was not supplied")
	}

	for s, resource := range state.devConfig.Resources {
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
func ListResourcesHandler(ctx context.Context, state *State, r *http.Request) (libapi.ListResourcesResponse, error) {
	resources := make([]libapi.Resource, 0, len(state.devConfig.RawResources))
	for slug, resource := range state.devConfig.Resources {
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
			Slug:              slug,
			Kind:              libapi.ResourceKind(resource.Kind()),
			KindConfig:        kindConfig,
			CanUseResource:    true,
			CanUpdateResource: true,
		})
	}

	return libapi.ListResourcesResponse{
		Resources: resources,
	}, nil
}

type UpdateResourceRequest struct {
	ID         string                          `json:"id"`
	Slug       string                          `json:"slug"`
	Name       string                          `json:"name"`
	Kind       resources.ResourceKind          `json:"kind"`
	KindConfig kind_configs.ResourceKindConfig `json:"kindConfig"`
}

type UpdateResourceResponse struct {
	ResourceID string `json:"resourceID"`
}

// UpdateResourceHandler handles requests to the /i/resources/get endpoint
func UpdateResourceHandler(ctx context.Context, state *State, r *http.Request, req UpdateResourceRequest) (UpdateResourceResponse, error) {
	// Check if resource exists in dev config file.
	var foundResource bool
	var oldSlug string
	var resource resources.Resource
	for configSlug, configResource := range state.devConfig.Resources {
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

	// Convert to internal representation of resource for updating.
	internalResource, err := conversion.ConvertToInternalResource(resource)
	if err != nil {
		return UpdateResourceResponse{}, errors.Wrap(err, "converting to external resource")
	}

	// Update internal resource - utilize KindConfig.Update to not overwrite sensitive fields.
	internalResource.Slug = req.Slug
	internalResource.Name = req.Name
	// Set a new resource ID based on the new slug.
	newResourceID := fmt.Sprintf("res-%s", req.Slug)
	internalResource.ID = newResourceID
	if err := internalResource.KindConfig.Update(req.KindConfig); err != nil {
		return UpdateResourceResponse{}, errors.Wrap(err, "updating kind config of internal resource")
	}

	// Convert back to external representation of resource.
	newResource, err := internalResource.ToExternalResource()
	if err != nil {
		return UpdateResourceResponse{}, errors.Wrap(err, "converting to external resource")
	}

	// Remove the old resource first - we need to do this since devConfig.Resources is a mapping from slug to resource,
	// and if the update resource request involves updating the slug, we don't want to leave the old resource (under the
	// old slug) in the dev config file.
	if err := state.devConfig.RemoveResource(oldSlug); err != nil {
		return UpdateResourceResponse{}, errors.Wrap(err, "deleting old resource")
	}

	if err := state.devConfig.SetResource(req.Slug, newResource); err != nil {
		return UpdateResourceResponse{}, errors.Wrap(err, "setting resource")
	}

	return UpdateResourceResponse{
		ResourceID: newResourceID,
	}, nil
}

type IsResourceSlugAvailableResponse struct {
	Available bool `json:"available"`
}

// IsResourceSlugAvailableHandler handles requests to the /i/resources/isSlugAvailable endpoint
func IsResourceSlugAvailableHandler(ctx context.Context, state *State, r *http.Request) (IsResourceSlugAvailableResponse, error) {
	slug := r.URL.Query().Get("slug")
	res, ok := state.devConfig.Resources[slug]
	return IsResourceSlugAvailableResponse{
		Available: !ok || res.ID() == r.URL.Query().Get("id"),
	}, nil
}

type ListPromptsResponse struct {
	Prompts []libapi.Prompt `json:"prompts"`
}

func ListPromptHandler(ctx context.Context, state *State, r *http.Request) (ListPromptsResponse, error) {
	runID := r.URL.Query().Get("runID")
	if runID == "" {
		return ListPromptsResponse{}, errors.New("runID is required")
	}

	run, ok := state.runs.get(runID)
	if !ok {
		return ListPromptsResponse{}, errors.New("run not found")
	}

	return ListPromptsResponse{Prompts: run.Prompts}, nil
}

type SubmitPromptRequest struct {
	ID          string                 `json:"id"`
	Values      map[string]interface{} `json:"values"`
	RunID       string                 `json:"runID"`
	SubmittedBy *string                `json:"-"`
}

func SubmitPromptHandler(ctx context.Context, state *State, r *http.Request, req SubmitPromptRequest) (PromptReponse, error) {
	if req.ID == "" {
		return PromptReponse{}, errors.New("prompt ID is required")
	}
	if req.RunID == "" {
		return PromptReponse{}, errors.New("run ID is required")
	}

	userID := state.cliConfig.ParseTokenForAnalytics().UserID

	_, err := state.runs.update(req.RunID, func(run *LocalRun) error {
		for i := range run.Prompts {
			if run.Prompts[i].ID == req.ID {
				now := time.Now()
				run.Prompts[i].SubmittedAt = &now
				run.Prompts[i].Values = req.Values
				run.Prompts[i].SubmittedBy = &userID
				return nil
			}
		}
		return errors.New("prompt does not exist")
	})
	if err != nil {
		return PromptReponse{}, err
	}
	return PromptReponse{ID: req.ID}, nil
}

type GetDescendantsResponse struct {
	Descendants []LocalRun `json:"descendants"`
}

func GetDescendantsHandler(ctx context.Context, state *State, r *http.Request) (GetDescendantsResponse, error) {
	runID := r.URL.Query().Get("runID")
	if runID == "" {
		return GetDescendantsResponse{}, errors.New("runID cannot be empty")
	}
	descendants := state.runs.getDescendants(runID)

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

func GetUserHandler(ctx context.Context, state *State, r *http.Request) (GetUserResponse, error) {
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
