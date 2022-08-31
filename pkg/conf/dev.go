package conf

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/AlecAivazis/survey/v2"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/utils"
	"github.com/airplanedev/lib/pkg/resources"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

var DefaultDevConfigFileName = "airplane.dev.yaml"

// DevConfig represents an airplane dev configuration.
type DevConfig struct {
	// Env contains all environment variables.
	Env map[string]string `json:"env" yaml:"env"`
	// RawResources is a list of resources that represents what the user sees in the dev config file.
	RawResources []map[string]interface{} `json:"resources" yaml:"resources"`

	// Path is the location that the dev config file was loaded from and where updates will be written to.
	Path string `json:"-" yaml:"-"`
	// Resources is a mapping from slug to external resource.
	Resources map[string]resources.Resource `json:"-" yaml:"-"`
}

// NewDevConfig returns a default dev config.
func NewDevConfig() *DevConfig {
	return &DevConfig{
		Env:          map[string]string{},
		RawResources: []map[string]interface{}{},
		Resources:    map[string]resources.Resource{},
		Path:         DefaultDevConfigFileName,
	}
}

func (d *DevConfig) updateRawResources() error {
	resourceList := make([]resources.Resource, 0, len(d.RawResources))

	for _, resource := range d.Resources {
		resourceList = append(resourceList, resource)
	}

	// TODO: Use json.Marshal/Unmarshal once we've added yaml struct tags to external resource structs.
	buf, err := json.Marshal(resourceList)
	if err != nil {
		return errors.Wrap(err, "marshaling resources")
	}

	d.RawResources = []map[string]interface{}{}
	if err := json.Unmarshal(buf, &d.RawResources); err != nil {
		return errors.Wrap(err, "unmarshalling into raw resources")
	}

	return nil
}

// SetResource updates a resource in the dev config file, creating it if necessary.
func (d *DevConfig) SetResource(slug string, resource resources.Resource) error {
	d.Resources[slug] = resource

	if err := d.updateRawResources(); err != nil {
		return errors.Wrap(err, "updating raw resources")
	}

	if err := WriteDevConfig(d); err != nil {
		return errors.Wrap(err, "writing dev config")
	}
	logger.Log("Wrote resource %s to dev config file at %s", slug, d.Path)

	return nil
}

// RemoveResource removes the resource in the dev config file with the given slug, if it exists, and returns whether or
// not the resource was removed.
func (d *DevConfig) RemoveResource(slug string) error {
	for s := range d.Resources {
		if s == slug {
			delete(d.Resources, s)
		}
	}

	if err := d.updateRawResources(); err != nil {
		return errors.Wrap(err, "updating raw resources")
	}

	if err := WriteDevConfig(d); err != nil {
		return errors.Wrap(err, "writing dev config")
	}

	return nil
}

func ReadDevConfig(path string) (*DevConfig, error) {
	cfg := &DevConfig{}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, ErrMissing
	}

	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrap(err, "read config")
	}

	if err := yaml.Unmarshal(buf, cfg); err != nil {
		return nil, errors.Wrap(err, "unmarshal config")
	}

	slugToResource := map[string]resources.Resource{}
	for _, r := range cfg.RawResources {
		kind, ok := r["kind"]
		if !ok {
			return nil, errors.New("missing kind property in resource")
		}

		kindStr, ok := kind.(string)
		if !ok {
			return nil, errors.Errorf("expected kind type to be string, got %T", kind)
		}

		slug, ok := r["slug"]
		if !ok {
			return nil, errors.New("missing slug property in resource")
		}

		slugStr, ok := r["slug"].(string)
		if !ok {
			return nil, errors.Errorf("expected slug type to be string, got %T", slug)
		}

		if slugToResource[slugStr], err = resources.GetResource(resources.ResourceKind(kindStr), r); err != nil {
			return nil, errors.Wrap(err, "getting resource from raw resource")
		}
	}

	cfg.Resources = slugToResource
	cfg.Path = path

	return cfg, nil
}

func WriteDevConfig(config *DevConfig) error {
	if err := os.MkdirAll(filepath.Dir(config.Path), 0777); err != nil {
		return errors.Wrap(err, "mkdir")
	}

	buf, err := yaml.Marshal(config)
	if err != nil {
		return err
	}

	if err := os.WriteFile(config.Path, buf, 0600); err != nil {
		return errors.Wrap(err, "write config")
	}

	return nil
}

func PromptDevConfigFileCreation(defaultPath string) (string, error) {
	ok, err := utils.Confirm("Dev config file not found - would you like to create one?")
	if err != nil {
		return "", err
	} else if !ok {
		return "", nil
	}

	var path string
	if err := survey.AskOne(
		&survey.Input{
			Message: "Where would you like to create the dev config file?",
			Default: defaultPath,
		},
		&path,
	); err != nil {
		return "", err
	}

	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	return path, nil
}
