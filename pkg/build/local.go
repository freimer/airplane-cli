package build

import (
	"context"
	"sync"

	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/configs"
	"github.com/airplanedev/cli/pkg/logger"
	libapi "github.com/airplanedev/lib/pkg/api"
	"github.com/airplanedev/lib/pkg/build"
	"github.com/pkg/errors"
)

type localBuildCreator struct {
	registryTokenGetter
}

func NewLocalBuildCreator() BuildCreator {
	return &localBuildCreator{}
}

func (d *localBuildCreator) CreateBuild(ctx context.Context, req Request) (*build.Response, error) {
	registry, err := d.getRegistryToken(ctx, req.Client)
	if err != nil {
		return nil, err
	}

	env, err := req.Def.GetEnv()
	if err != nil {
		return nil, err
	}
	buildEnv, err := getBuildEnv(ctx, req.Client, env)
	if err != nil {
		return nil, err
	}

	kind, _, err := req.Def.GetKindAndOptions()
	if err != nil {
		return nil, err
	}

	buildConfig, err := req.Def.GetBuildConfig()
	if err != nil {
		return nil, err
	}

	if req.Shim {
		buildConfig["shim"] = "true"
	}

	b, err := build.New(build.LocalConfig{
		Root:    req.Root,
		Builder: string(kind),
		// TODO(fleung): should be build.BuildConfig, not build.KindOptions
		Options: build.KindOptions(buildConfig),
		Auth: &build.RegistryAuth{
			Token: registry.Token,
			Repo:  registry.Repo,
		},
		BuildArgs: buildEnv,
	})
	if err != nil {
		return nil, errors.Wrap(err, "new build")
	}
	defer b.Close()

	logger.Log("Building...")
	resp, err := b.Build(ctx, req.TaskID, "latest")
	if err != nil {
		return nil, errors.Wrap(err, "build")
	}

	logger.Log("Pushing...")
	if err := b.Push(ctx, resp.ImageURL); err != nil {
		return nil, errors.Wrap(err, "push")
	}

	return resp, nil
}

// Retrieves a build env from def - looks for env vars starting with BUILD_ and either uses the
// string literal or looks up the config value.
func getBuildEnv(ctx context.Context, client api.APIClient, taskEnv libapi.TaskEnv) (map[string]string, error) {
	buildEnv := make(map[string]string)
	for k, v := range taskEnv {
		if v.Value != nil {
			buildEnv[k] = *v.Value
		} else if v.Config != nil {
			nt, err := configs.ParseName(*v.Config)
			if err != nil {
				return nil, err
			}
			res, err := client.GetConfig(ctx, api.GetConfigRequest{
				Name:       nt.Name,
				Tag:        nt.Tag,
				ShowSecret: true,
			})
			if err != nil {
				return nil, err
			}
			buildEnv[k] = res.Config.Value
		}
	}
	return buildEnv, nil
}

// registryTokenGetter gets registry tokens and is optimized for concurrent requests.
type registryTokenGetter struct {
	getRegistryTokenMutex sync.Mutex
	cachedRegistryToken   *api.RegistryTokenResponse
}

func (d *registryTokenGetter) getRegistryToken(ctx context.Context, client api.APIClient) (registryToken api.RegistryTokenResponse, err error) {
	d.getRegistryTokenMutex.Lock()
	defer d.getRegistryTokenMutex.Unlock()
	if d.cachedRegistryToken != nil {
		registryToken = *d.cachedRegistryToken
	} else {
		registryToken, err = client.GetRegistryToken(ctx)
		if err != nil {
			return registryToken, errors.Wrap(err, "getting registry token")
		}
		d.cachedRegistryToken = &registryToken
	}
	return registryToken, nil
}
