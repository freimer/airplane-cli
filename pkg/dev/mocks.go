package dev

import (
	"context"

	"github.com/airplanedev/cli/pkg/api"
	"github.com/stretchr/testify/mock"
)

// MockExecutor is a mock implementation of the Executor interface.
type MockExecutor struct {
	mock.Mock
}

func (m *MockExecutor) Execute(ctx context.Context, config ExecutionConfig) (api.Outputs, error) {
	args := m.Called(ctx, config)
	return api.Outputs{}, args.Error(0)
}
