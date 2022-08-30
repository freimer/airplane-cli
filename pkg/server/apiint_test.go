package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/cli"
	"github.com/airplanedev/cli/pkg/conf"
	libapi "github.com/airplanedev/lib/pkg/api"
	"github.com/airplanedev/lib/pkg/deploy/discover"
	"github.com/airplanedev/lib/pkg/resources"
	"github.com/airplanedev/lib/pkg/resources/kinds"
	"github.com/stretchr/testify/require"
)

func TestListResources(t *testing.T) {
	require := require.New(t)
	h := getHttpExpect(
		context.Background(),
		t,
		newRouter(&State{
			devConfig: &conf.DevConfig{
				Resources: map[string]resources.Resource{
					"db": kinds.PostgresResource{
						BaseResource: resources.BaseResource{
							Slug: "db",
						},
					},
					"slack": kinds.SlackResource{
						BaseResource: resources.BaseResource{
							Slug: "slack",
						},
					},
				},
			},
		}),
	)

	body := h.GET("/i/resources/list").
		Expect().
		Status(http.StatusOK).Body()

	var resp libapi.ListResourcesResponse
	err := json.Unmarshal([]byte(body.Raw()), &resp)
	require.NoError(err)
	require.ElementsMatch([]libapi.Resource{
		{
			Slug: "db",
		},
		{
			Slug: "slack",
		},
	}, resp.Resources)
}

func TestSubmitPrompts(t *testing.T) {
	require := require.New(t)
	taskSlug := "task1"
	now := time.Now()
	runID := "run_0"
	promptID1 := GenerateID("pmt")
	prompt1 := libapi.Prompt{
		ID:    promptID1,
		RunID: runID,
		Schema: libapi.Parameters{{
			Name: "option1",
			Slug: "option1",
			Type: "string",
		}},
		CreatedAt: now,
	}

	prompt2 := libapi.Prompt{
		ID:    "prompt2",
		RunID: runID,
		Schema: libapi.Parameters{{
			Name: "option2",
			Slug: "option2",
			Type: "boolean",
		}},
		CreatedAt: now,
	}

	runstore := NewRunStore()
	run := LocalRun{
		RunID:    runID,
		TaskName: taskSlug,
		Outputs:  api.Outputs{V: "run0"},
		Prompts:  []libapi.Prompt{prompt1, prompt2},
	}
	runstore.add(taskSlug, runID, run)

	h := getHttpExpect(
		context.Background(),
		t,
		newRouter(&State{
			runs:        runstore,
			taskConfigs: map[string]discover.TaskConfig{},
			devConfig:   &conf.DevConfig{},
			cliConfig:   &cli.Config{Client: &api.Client{}},
		}),
	)

	var resp PromptReponse
	values := map[string]interface{}{"option1": "blah"}

	body := h.POST("/i/prompts/submit").
		WithJSON(SubmitPromptRequest{
			RunID:  runID,
			ID:     promptID1,
			Values: values,
		}).
		Expect().
		Status(http.StatusOK).Body()

	err := json.Unmarshal([]byte(body.Raw()), &resp)
	require.NoError(err)
	require.Equal(promptID1, resp.ID)

	body = h.GET("/i/prompts/list").
		WithQuery("runID", runID).
		Expect().
		Status(http.StatusOK).Body()

	listPrompts := ListPromptsResponse{}
	err = json.Unmarshal([]byte(body.Raw()), &listPrompts)
	require.NoError(err)
	require.Equal(2, len(listPrompts.Prompts))

	// check prompt 1 values match what was submitted
	require.Equal(values, listPrompts.Prompts[0].Values)
	require.NotNil(listPrompts.Prompts[0].SubmittedAt)
	require.NotNil(listPrompts.Prompts[0].SubmittedBy)

	// prompt 2 should be unsubmitted
	require.Equal(prompt2.ID, listPrompts.Prompts[1].ID)
	require.Nil(listPrompts.Prompts[1].Values)
	require.Nil(listPrompts.Prompts[1].SubmittedAt)
	require.Nil(listPrompts.Prompts[1].SubmittedBy)

}
