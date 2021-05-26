package main

import (
	"context"
	"os"

	"github.com/airplanedev/cli/pkg/cmd/root"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/trap"
	"github.com/airplanedev/cli/pkg/utils"
	"github.com/pkg/errors"
	_ "github.com/segmentio/events/v2/text"
)

var (
	version = "<dev>"
)

func main() {
	var cmd = root.New()
	var ctx = trap.Context()

	cmd.Version = version

	if err := cmd.ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			// TODO(amir): output operation canceled?
			return
		}

		if logger.EnableDebug {
			logger.Debug("Error: %+v", err)
		}
		logger.Log("")
		if exerr, ok := errors.Cause(err).(utils.ErrorExplained); ok {
			logger.Log(logger.Red(exerr.Error()))
			logger.Log("")
			logger.Log(exerr.ExplainError())
		} else {
			logger.Error(errors.Cause(err).Error())
		}
		logger.Log("")

		os.Exit(1)
	}
}
