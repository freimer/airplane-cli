package conf

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/airplanedev/cli/pkg/dev/env"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/lib/pkg/resources"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

var DefaultDevConfigFileName = "airplane.dev.yaml"

// DevConfig represents an airplane dev configuration.
type DevConfig struct {
	// Configs contains all of the config variables.
	ConfigVars map[string]string `json:"configVars" yaml:"configVars"`

	// RawResources is a list of resources that represents what the user sees in the dev config file.
	RawResources []map[string]interface{} `json:"resources" yaml:"resources"`

	// Path is the location that the dev config file was loaded from and where updates will be written to.
	Path string `json:"-" yaml:"-"`
	// Resources is a mapping from slug to external resource.
	Resources map[string]env.ResourceWithEnv `json:"-" yaml:"-"`
}

// NewDevConfig returns a default dev config.
func NewDevConfig(path string) *DevConfig {
	return &DevConfig{
		ConfigVars:   map[string]string{},
		RawResources: []map[string]interface{}{},
		Resources:    map[string]env.ResourceWithEnv{},
		Path:         path,
	}
}

func (d *DevConfig) updateRawResources() error {
	resourceList := make([]resources.Resource, 0, len(d.RawResources))

	for _, r := range d.Resources {
		resourceList = append(resourceList, r.Resource)
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
func (d *DevConfig) SetResource(slug string, r resources.Resource) error {
	d.Resources[slug] = env.ResourceWithEnv{
		Resource: r,
		Remote:   false,
	}

	if err := d.updateRawResources(); err != nil {
		return errors.Wrap(err, "updating raw resources")
	}

	if err := WriteDevConfig(d); err != nil {
		return errors.Wrap(err, "writing dev config")
	}
	logger.Log("Wrote resource %s to dev config file at %s", slug, d.Path)

	return nil
}

// RemoveResource removes the resource in the dev config file with the given slug, if it exists.
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

	slugToResource := map[string]env.ResourceWithEnv{}
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

		res, err := resources.GetResource(resources.ResourceKind(kindStr), r)
		if err != nil {
			return nil, errors.Wrap(err, "getting resource from raw resource")
		}
		slugToResource[slugStr] = env.ResourceWithEnv{
			Resource: res,
			Remote:   false,
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

	if _, err := os.Stat(config.Path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			f, createErr := os.Create(config.Path)
			if createErr != nil {
				return errors.Wrap(createErr, "creating dev config file")
			}
			f.Close()
			logger.Log("Created dev config file at %s", config.Path)
		} else {
			return errors.Wrap(err, "checking if dev config file exists")
		}
	}

	if err := os.WriteFile(config.Path, buf, 0644); err != nil {
		return errors.Wrap(err, "write config")
	}

	return nil
}

// LoadDevConfigFile attempts to load in the dev config file at devConfigPath.
func LoadDevConfigFile(devConfigPath string) (*DevConfig, error) {
	var devConfig *DevConfig
	var devConfigLoaded bool
	if devConfigPath != "" {
		var err error
		devConfig, err = ReadDevConfig(devConfigPath)
		if err != nil {
			if !errors.Is(err, ErrMissing) {
				return nil, errors.Wrap(err, "unable to read dev config")
			}
		} else {
			devConfigLoaded = true
		}
	}

	if devConfigLoaded {
		logger.Log("Loaded dev config from %s", devConfigPath)
	} else {
		logger.Debug("Using empty dev config")
		devConfig = NewDevConfig(devConfigPath)
	}

	return devConfig, nil
}
