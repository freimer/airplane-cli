package resource

import (
	"github.com/airplanedev/lib/pkg/resources"
	"github.com/pkg/errors"
)

// GenerateAliasToResourceMap generates a mapping from alias to resource - resourceAttachments is a mapping from alias
// to slug, and slugToResource is a mapping from slug to resource, and so we just link the two.
func GenerateAliasToResourceMap(
	resourceAttachments map[string]string,
	slugToResource map[string]resources.Resource,
) (map[string]resources.Resource, error) {
	aliasToResourceMap := map[string]resources.Resource{}
	// We only need to generate entries in the map for resources that are explicitly attached to a task.
	for alias, slug := range resourceAttachments {
		resource, ok := slugToResource[slug]
		if !ok {
			// TODO: Augment error message with airplane subcommand to add resource
			return nil, errors.Errorf("Cannot find resource with slug %s in dev config file", slug)
		}
		aliasToResourceMap[alias] = resource
	}

	return aliasToResourceMap, nil
}

// Creates a map of the resource slug to resource ID
func GenerateResourceSlugToID(resources map[string]resources.Resource) map[string]string {
	runResources := map[string]string{}
	for slug, r := range resources {
		runResources[slug] = r.ID()
	}
	return runResources
}
