package resource

import (
	"testing"

	"github.com/airplanedev/lib/pkg/resources/kind_configs"
	"github.com/stretchr/testify/require"
)

func TestKindConfigToMap(t *testing.T) {
	require := require.New(t)
	kindConfig := kind_configs.NewPostgresConfigOptions{
		Host:       "localhost",
		Port:       "123",
		Database:   "test-db",
		Username:   "user",
		Password:   "pass",
		DisableSSL: true,
	}
	r := kind_configs.InternalResource{
		ID:   "res-1",
		Slug: "my-resource",
		Name: "my resource name",
		Kind: kind_configs.KindPostgres,
		KindConfig: kind_configs.ResourceKindConfig{
			Postgres: kind_configs.NewPostgresKindConfig(kindConfig),
		},
	}
	res, err := KindConfigToMap(r)
	require.NoError(err)
	expect := map[string]interface{}{"database": "test-db",
		"disableSSL":    true,
		"host":          "localhost",
		"username":      "user",
		"password":      "pass",
		"port":          "123",
		"sshHost":       "",
		"sshPort":       "",
		"sshPrivateKey": "",
		"sshUsername":   ""}

	require.Equal(res["postgres"], expect)
}
