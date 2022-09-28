package apidev_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/airplanedev/cli/pkg/server"
	"github.com/airplanedev/cli/pkg/server/apidev"
	"github.com/airplanedev/cli/pkg/server/state"
	"github.com/airplanedev/cli/pkg/server/test_utils"
	"github.com/airplanedev/cli/pkg/version"
	"github.com/airplanedev/lib/pkg/deploy/discover"
	"github.com/airplanedev/lib/pkg/deploy/taskdir/definitions"
	"github.com/stretchr/testify/require"
)

func TestVersion(t *testing.T) {
	require := require.New(t)

	h := test_utils.GetHttpExpect(
		context.Background(),
		t,
		server.NewRouter(&state.State{}),
	)

	body := h.GET("/dev/version").
		Expect().
		Status(http.StatusOK).Body()

	var resp version.Metadata
	err := json.Unmarshal([]byte(body.Raw()), &resp)
	require.NoError(err)

	require.Equal(resp.Status, "ok")
	require.NotEmpty(resp.Status)

}

func TestListEntrypoints(t *testing.T) {
	require := require.New(t)

	taskSlug := "my_task"
	taskDefinition := &definitions.Definition_0_3{
		Name: "My task",
		Slug: taskSlug,
		Node: &definitions.NodeDefinition_0_3{
			Entrypoint:  "my_task.ts",
			NodeVersion: "18",
		},
	}
	taskDefinition.SetDefnFilePath("my_task.task.yaml")

	viewSlug := "my_view"
	viewDefinition := definitions.ViewDefinition{
		Name:       "My view",
		Entrypoint: "my_view.ts",
		Slug:       viewSlug,
	}

	h := test_utils.GetHttpExpect(
		context.Background(),
		t,
		server.NewRouter(&state.State{
			TaskConfigs: map[string]discover.TaskConfig{
				taskSlug: {
					TaskID:         "tsk123",
					TaskRoot:       ".",
					TaskEntrypoint: "my_task.ts",
					Def:            taskDefinition,
					Source:         discover.ConfigSourceDefn,
				},
			},
			ViewConfigs: map[string]discover.ViewConfig{
				viewSlug: {
					Def:    viewDefinition,
					Source: discover.ConfigSourceDefn,
				},
			},
		}),
	)

	body := h.GET("/dev/list").
		Expect().
		Status(http.StatusOK).Body()

	var resp apidev.ListEntrypointsHandlerResponse
	err := json.Unmarshal([]byte(body.Raw()), &resp)
	require.NoError(err)
	require.Equal(map[string][]apidev.AppMetadata{
		"my_task.ts": {
			{
				Name: "My task",
				Slug: "my_task",
				Kind: apidev.AppKindTask,
			},
		},
		"my_view.ts": {
			{
				Name: "My view",
				Slug: "my_view",
				Kind: apidev.AppKindView,
			},
		},
	}, resp.Entrypoints)
}
