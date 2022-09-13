package apiext_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/cli"
	"github.com/airplanedev/cli/pkg/conf"
	"github.com/airplanedev/cli/pkg/dev"
	"github.com/airplanedev/cli/pkg/dev/logs"
	"github.com/airplanedev/cli/pkg/server"
	"github.com/airplanedev/cli/pkg/server/apiext"
	"github.com/airplanedev/cli/pkg/server/state"
	"github.com/airplanedev/cli/pkg/server/test_utils"
	"github.com/airplanedev/lib/pkg/build"
	"github.com/airplanedev/lib/pkg/deploy/discover"
	"github.com/airplanedev/lib/pkg/deploy/taskdir/definitions"
	"github.com/airplanedev/lib/pkg/resources"
	"github.com/airplanedev/lib/pkg/resources/kinds"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestExecute(t *testing.T) {
	require := require.New(t)
	mockExecutor := new(dev.MockExecutor)
	slug := "my_task"

	taskDefinition := &definitions.Definition_0_3{
		Name: "My Task",
		Slug: slug,
		Node: &definitions.NodeDefinition_0_3{
			Entrypoint:  "my_task.ts",
			NodeVersion: "18",
		},
	}
	taskDefinition.SetDefnFilePath("my_task.task.yaml")

	runID := "run1234"
	logBroker := logs.NewDevLogBroker()
	store := state.NewRunStore()
	store.Add(slug, runID, dev.LocalRun{
		RunID:     runID,
		LogBroker: logBroker,
	},
	)
	cliConfig := &cli.Config{Client: &api.Client{}}
	h := test_utils.GetHttpExpect(
		context.Background(),
		t,
		server.NewRouter(&state.State{
			CliConfig: cliConfig,
			EnvSlug:   "stage",
			Executor:  mockExecutor,
			Port:      1234,
			Runs:      store,
			TaskConfigs: map[string]discover.TaskConfig{
				slug: {
					TaskID:         "tsk123",
					TaskRoot:       ".",
					TaskEntrypoint: "my_task.ts",
					Def:            taskDefinition,
					Source:         discover.ConfigSourceDefn,
				},
			},
			DevConfig: &conf.DevConfig{},
		}),
	)

	paramValues := api.Values{
		"param1": "a",
		"param2": "b",
	}

	runConfig := dev.LocalRunConfig{
		ID:   runID,
		Name: "My Task",
		Root: cliConfig,
		Kind: build.TaskKindNode,
		KindOptions: build.KindOptions{
			"entrypoint":  "my_task.ts",
			"nodeVersion": "18",
		},
		ParamValues: paramValues,
		Port:        1234,
		File:        "my_task.ts",
		Slug:        slug,
		Env:         map[string]string{},
		EnvSlug:     "stage",
		Resources:   map[string]resources.Resource{},
		LogBroker:   logBroker,
	}
	mockExecutor.On("Execute", mock.Anything, runConfig).Return(nil)

	body := h.POST("/v0/tasks/execute").
		WithJSON(apiext.ExecuteTaskRequest{
			Slug:        slug,
			ParamValues: paramValues,
			RunID:       runID,
		}).
		Expect().
		Status(http.StatusOK).Body()

	mockExecutor.AssertCalled(t, "Execute", mock.Anything, runConfig)

	var resp dev.LocalRun
	err := json.Unmarshal([]byte(body.Raw()), &resp)
	require.NoError(err)
	require.True(strings.HasPrefix(resp.RunID, "run"))
}

func TestExecuteBuiltin(t *testing.T) {
	require := require.New(t)
	mockExecutor := new(dev.MockExecutor)
	slug := "airplane:sql_query"

	taskDefinition := &definitions.Definition_0_3{
		Name: "My Task",
		Slug: slug,
		Node: &definitions.NodeDefinition_0_3{
			Entrypoint:  "my_task.ts",
			NodeVersion: "18",
		},
		Resources: definitions.ResourceDefinition_0_3{Attachments: map[string]string{"my_db": "database"}},
	}
	taskDefinition.SetDefnFilePath("my_task.task.yaml")

	runID := "run1234"
	logBroker := logs.NewDevLogBroker()
	store := state.NewRunStore()
	store.Add(slug, runID, dev.LocalRun{
		RunID:     runID,
		LogBroker: logBroker,
	},
	)
	cliConfig := &cli.Config{Client: &api.Client{}}
	dbResource := kinds.PostgresResource{
		BaseResource: resources.BaseResource{
			Kind: kinds.ResourceKindPostgres,
			Slug: "database",
			ID:   "res-database",
		},
		Username: "postgres",
		Password: "password",
	}

	h := test_utils.GetHttpExpect(
		context.Background(),
		t,
		server.NewRouter(&state.State{
			CliConfig: cliConfig,
			EnvSlug:   "stage",
			Executor:  mockExecutor,
			Port:      1234,
			Runs:      store,
			TaskConfigs: map[string]discover.TaskConfig{
				slug: {
					TaskID:         "tsk123",
					TaskRoot:       ".",
					TaskEntrypoint: "my_task.ts",
					Def:            taskDefinition,
					Source:         discover.ConfigSourceDefn,
				},
			},
			DevConfig: &conf.DevConfig{Resources: map[string]resources.Resource{
				"database": &dbResource,
			}},
		}),
	)

	paramValues := api.Values{
		"query":           "select * from users limit 1",
		"transactionMode": "auto",
	}

	runConfig := dev.LocalRunConfig{
		ID:          runID,
		Root:        cliConfig,
		ParamValues: paramValues,
		Port:        1234,
		Slug:        slug,
		EnvSlug:     "stage",
		Resources: map[string]resources.Resource{
			"db": &dbResource,
		},
		IsBuiltin: true,
		LogBroker: logBroker,
	}
	mockExecutor.On("Execute", mock.Anything, runConfig).Return(nil)

	body := h.POST("/v0/tasks/execute").
		WithJSON(apiext.ExecuteTaskRequest{
			Slug:        slug,
			ParamValues: paramValues,
			RunID:       runID,
			Resources:   map[string]string{"db": "res-database"},
		}).
		Expect().
		Status(http.StatusOK).Body()

	mockExecutor.AssertCalled(t, "Execute", mock.Anything, runConfig)

	var resp dev.LocalRun
	err := json.Unmarshal([]byte(body.Raw()), &resp)
	require.NoError(err)
	require.True(strings.HasPrefix(resp.RunID, "run"))
}

func TestGetRun(t *testing.T) {
	require := require.New(t)

	runID := "run1234"
	runstore := state.NewRunStore()
	runstore.Add("task1", runID, dev.LocalRun{Status: api.RunSucceeded, RunID: runID})
	h := test_utils.GetHttpExpect(
		context.Background(),
		t,
		server.NewRouter(&state.State{
			Runs:        runstore,
			TaskConfigs: map[string]discover.TaskConfig{},
		}),
	)
	body := h.GET("/v0/runs/get").
		WithQuery("id", runID).
		Expect().
		Status(http.StatusOK).Body()

	var resp dev.LocalRun
	err := json.Unmarshal([]byte(body.Raw()), &resp)
	require.NoError(err)

	require.Equal(runID, resp.RunID)
	require.Equal(api.RunSucceeded, resp.Status)
}

func TestGetOutput(t *testing.T) {
	require := require.New(t)
	runID := "run1234"

	runstore := state.NewRunStore()
	runstore.Add("task1", runID, dev.LocalRun{Outputs: api.Outputs{V: "hello"}})
	h := test_utils.GetHttpExpect(
		context.Background(),
		t,
		server.NewRouter(&state.State{
			Runs:        runstore,
			TaskConfigs: map[string]discover.TaskConfig{},
		}),
	)

	body := h.GET("/v0/runs/getOutputs").
		WithQuery("id", runID).
		Expect().
		Status(http.StatusOK).Body()

	var resp apiext.GetOutputsResponse
	err := json.Unmarshal([]byte(body.Raw()), &resp)
	require.NoError(err)
	require.Equal(api.Outputs{
		V: "hello",
	}, resp.Output)
}

func TestListRuns(t *testing.T) {
	require := require.New(t)
	taskSlug := "task1"

	runstore := state.NewRunStore()
	testRuns := []dev.LocalRun{
		{RunID: "run_0", TaskID: taskSlug, Outputs: api.Outputs{V: "run0"}},
		{RunID: "run_1", TaskID: taskSlug, Outputs: api.Outputs{V: "run1"}, CreatorID: "user1"},
		{RunID: "run_2", TaskID: taskSlug, Outputs: api.Outputs{V: "run2"}},
	}
	for i, run := range testRuns {
		runstore.Add(taskSlug, fmt.Sprintf("run_%v", i), run)

	}
	h := test_utils.GetHttpExpect(
		context.Background(),
		t,
		server.NewRouter(&state.State{
			Runs:        runstore,
			TaskConfigs: map[string]discover.TaskConfig{},
		}),
	)
	var resp apiext.ListRunsResponse
	body := h.GET("/v0/runs/list").
		WithQuery("taskSlug", taskSlug).
		Expect().
		Status(http.StatusOK).Body()
	err := json.Unmarshal([]byte(body.Raw()), &resp)
	require.NoError(err)

	for i := range resp.Runs {
		// runHistory is ordered by most recent
		require.EqualValues(resp.Runs[i], testRuns[len(testRuns)-i-1])
	}
}
