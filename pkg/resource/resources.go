package resource

import (
	"context"

	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/dev/env"
	"github.com/airplanedev/cli/pkg/server/state"
	libapi "github.com/airplanedev/lib/pkg/api"
	"github.com/airplanedev/lib/pkg/resources"
	"github.com/airplanedev/lib/pkg/resources/kinds"
	"github.com/pkg/errors"
)

const slackID = "res00000000zteamslack"
const slackSlug = "team_slack"
const slackName = "Slack"

// This is not guaranteed to be the slug of the demo db, but should be in all cases where demo db creation doesn't
// fail during team creation.
const demoDBSlug = "demo_db"
const demoDBName = "[Demo DB]"

// GenerateAliasToResourceMap generates a mapping from alias to resource - resourceAttachments is a mapping from alias
// to slug, and slugToResource is a mapping from slug to resource, and so we just link the two.
func GenerateAliasToResourceMap(
	ctx context.Context,
	state *state.State,
	resourceAttachments map[string]string,
	slugToResource map[string]env.ResourceWithEnv,
) (map[string]resources.Resource, error) {
	aliasToResourceMap := map[string]resources.Resource{}
	// We only need to generate entries in the map for resources that are explicitly attached to a task.
	for alias, slug := range resourceAttachments {
		var resource resources.Resource
		resourceWithEnv, ok := slugToResource[slug]
		if !ok {
			return nil, errors.Errorf("Cannot find resource with slug %s in dev config file or remotely", slug)
		} else if resourceWithEnv.Remote {
			var envSlug string
			// We load in some default remote resources (e.g. Slack) - in those cases, the remote flag will be true,
			// but the envID/slug will still be "local", which is not a valid remote environment. In these cases, we
			// keep the env slug empty, which will default to the user's team's default environment.
			if state.EnvID != env.LocalEnvID {
				envSlug = state.EnvSlug
			}
			remoteResourceWithCredentials, err := state.CliConfig.Client.GetResource(ctx, api.GetResourceRequest{
				Slug:                 slug,
				EnvSlug:              envSlug,
				IncludeSensitiveData: true,
			})
			if err != nil {
				return nil, errors.Wrap(err, "getting resource with credentials")
			}
			resource = remoteResourceWithCredentials.ExportResource
		} else {
			resource = resourceWithEnv.Resource
		}

		aliasToResourceMap[alias] = resource
	}

	return aliasToResourceMap, nil
}

// MergeRemoteResources merges the resources defined in the dev config file with remote resources from the env passed
// in the local dev server on startup.
func MergeRemoteResources(ctx context.Context, state *state.State) (map[string]env.ResourceWithEnv, error) {
	mergedResources := make(map[string]env.ResourceWithEnv)
	if state == nil {
		return mergedResources, nil
	}

	for slug, res := range state.DevConfig.Resources {
		mergedResources[slug] = res
	}

	if state.EnvID != env.LocalEnvID {
		remoteResources, err := ListRemoteResources(ctx, state)
		if err != nil {
			return nil, errors.Wrap(err, "listing remote resources")
		}

		for _, res := range remoteResources {
			if _, ok := mergedResources[res.Slug]; !ok {
				mergedResources[res.Slug] = env.ResourceWithEnv{
					Resource: res.ExportResource,
					Remote:   true,
				}
			}
		}
	}

	// Always add Slack resource for convenience since at most one can exist in a team's remote environment.
	if _, ok := mergedResources[slackSlug]; !ok {
		mergedResources[slackSlug] = env.ResourceWithEnv{
			Resource: &kinds.SlackResource{
				BaseResource: resources.BaseResource{
					Kind: kinds.ResourceKindSlack,
					ID:   slackID,
					Slug: slackSlug,
					Name: slackName,
				},
			},
			Remote: true,
		}
	}

	// Also add demo DB for convenience, which is required by the getting started guides.
	if _, ok := mergedResources[demoDBSlug]; !ok {
		// Unlike the Slack resource above, the demo db does not have a fixed resource id, and so we get the resource
		// by slug here.
		demoDB, err := state.CliConfig.Client.GetResource(ctx, api.GetResourceRequest{
			Slug: demoDBSlug,
		})
		if err == nil {
			mergedResources[demoDBSlug] = env.ResourceWithEnv{
				Resource: &kinds.PostgresResource{
					BaseResource: resources.BaseResource{
						Kind: kinds.ResourceKindPostgres,
						ID:   demoDB.ID,
						Slug: demoDBSlug,
						Name: demoDBName,
					},
				},
				Remote: true,
			}
		} else {
			// If demo_db resource isn't found, don't error.
			if !errors.As(err, &libapi.ResourceMissingError{}) {
				return nil, errors.Wrap(err, "getting demo db resource")
			}
		}
	}

	return mergedResources, nil
}

func ListRemoteResources(ctx context.Context, state *state.State) ([]libapi.Resource, error) {
	resp, err := state.CliConfig.Client.ListResources(ctx, state.EnvSlug)
	if err != nil {
		return nil, err
	}
	return resp.Resources, nil
}

// Creates a map of the resource alias to resource ID
func GenerateResourceAliasToID(resourceAliases map[string]resources.Resource) map[string]string {
	resourceAliasToID := map[string]string{}
	for alias, resource := range resourceAliases {
		resourceAliasToID[alias] = resource.ID()
	}
	return resourceAliasToID
}

func SlugFromID(id string) (string, error) {
	if len(id) <= 4 {
		return "", errors.New("id must be of the form res-{slug}")
	}

	return id[4:], nil
}
