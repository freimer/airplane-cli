package server

import (
	"os"
	"sync"

	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/cli"
	"github.com/airplanedev/cli/pkg/conf"
	"github.com/airplanedev/cli/pkg/dev"
	"github.com/airplanedev/lib/pkg/deploy/discover"
	"github.com/airplanedev/lib/pkg/utils/logger"
	"github.com/pkg/errors"
)

type State struct {
	cliConfig   *cli.Config
	localClient *api.Client
	// Directory from which tasks and views were discovered
	dir      string
	envSlug  string
	executor dev.Executor
	port     int
	runs     *runsStore
	// Mapping from task slug to task config
	taskConfigs map[string]discover.TaskConfig
	// Mapping from view slug to view config
	viewConfigs map[string]discover.ViewConfig
	devConfig   *conf.DevConfig
	viteProcess *os.Process
	viteMutex   sync.Mutex
	logger      logger.Logger
}

// TODO: add limit on max items
type runsStore struct {
	// All runs
	runs map[string]LocalRun
	// A task's previous runs
	runHistory map[string][]string
	// A run's descendants
	runDescendants map[string][]string

	mu sync.Mutex
}

func NewRunStore() *runsStore {
	r := &runsStore{
		runs:           map[string]LocalRun{},
		runHistory:     map[string][]string{},
		runDescendants: map[string][]string{},
	}
	return r
}

func contains(runID string, history []string) bool {
	for _, id := range history {
		if id == runID {
			return true
		}
	}
	return false
}

func (store *runsStore) add(taskID string, runID string, run LocalRun) {
	store.mu.Lock()
	defer store.mu.Unlock()
	run.RunID = runID
	run.TaskName = taskID
	store.runs[runID] = run
	if _, ok := store.runHistory[taskID]; !ok {
		store.runHistory[taskID] = make([]string, 0)
	}
	if !contains(runID, store.runHistory[taskID]) {
		store.runHistory[taskID] = append([]string{runID}, store.runHistory[taskID]...)
	}

	if run.ParentID != "" {
		// attach run to parent
		store.runDescendants[run.ParentID] = append(store.runDescendants[run.ParentID], runID)
	}
}

func (store *runsStore) get(runID string) (LocalRun, bool) {
	res, ok := store.runs[runID]
	return res, ok
}

func (store *runsStore) getDescendants(runID string) []LocalRun {
	descendants := []LocalRun{}
	descIDs, ok := store.runDescendants[runID]
	if !ok {
		return []LocalRun{}
	}
	for _, descID := range descIDs {
		descendants = append(descendants, store.runs[descID])
	}
	return descendants
}

func (store *runsStore) update(runID string, f func(run *LocalRun) error) (LocalRun, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	res, ok := store.runs[runID]
	if !ok {
		return LocalRun{}, errors.Errorf("run with id %q not found", runID)
	}
	if err := f(&res); err != nil {
		return LocalRun{}, err
	}
	store.runs[runID] = res

	return res, nil
}

func (store *runsStore) getRunHistory(taskID string) []LocalRun {
	runIDs := store.runHistory[taskID]
	res := make([]LocalRun, len(runIDs))
	for i, runID := range runIDs {
		res[i] = store.runs[runID]
	}

	return res
}
