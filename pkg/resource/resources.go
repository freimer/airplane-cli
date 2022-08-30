package resource

import (
	"encoding/json"

	"github.com/airplanedev/lib/pkg/resources"
	"github.com/airplanedev/lib/pkg/resources/kind_configs"
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

// Creates a map of the resource alias to resource ID
func GenerateResourceAliasToID(resourceAliases map[string]resources.Resource) map[string]string {
	resourceAliasToID := map[string]string{}
	for alias, resource := range resourceAliases {
		resourceAliasToID[alias] = resource.ID()
	}
	return resourceAliasToID
}

func KindConfigToMap(r kind_configs.InternalResource) (map[string]interface{}, error) {
	b, err := json.Marshal(r.KindConfig)
	if err != nil {
		return nil, errors.Wrap(err, "marshaling KindConfig")
	}
	kindConfig := map[string]interface{}{}
	if err := json.Unmarshal(b, &kindConfig); err != nil {
		return nil, errors.Wrap(err, "unmarshaling KindConfig")
	}
	return kindConfig, nil
}
