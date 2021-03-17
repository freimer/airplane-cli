package login

import (
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/airplanedev/cli/pkg/browser"
	"github.com/airplanedev/cli/pkg/cli"
	"github.com/airplanedev/cli/pkg/conf"
	"github.com/airplanedev/cli/pkg/token"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// New returns a new login command.
func New(c *cli.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Login to Airplane",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), c)
		},
	}
	return cmd
}

// Run runs the login command.
func run(ctx context.Context, c *cli.Config) error {
	cfg, err := conf.ReadDefault()

	if errors.Is(err, conf.ErrMissing) {
		srv, err := token.NewServer(ctx)
		if err != nil {
			return err
		}
		defer srv.Close()

		open(c.Client.LoginURL(srv.URL()))

		select {
		case <-ctx.Done():
			return ctx.Err()

		case token := <-srv.Token():
			cfg.Token = token
		}

		if err := conf.WriteDefault(cfg); err != nil {
			return err
		}
	}

	fmt.Printf("You're all set!\n\nTo see what tasks you can run, try `$ airplane list`\n")
	return nil
}

// Open attempts to open the URL in the browser.
//
// As a special case, if `AP_BROWSER` env var is set to `none`
// the command will always print the URL.
func open(url string) {
	if os.Getenv("AP_BROWSER") != "none" {
		if err := browser.Open(runtime.GOOS, url); err == nil {
			return
		}
	}

	fmt.Printf("Visit %s to complete logging in\n", url)
}