package execute

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"time"

	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/cli"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// Config are the execute config.
type config struct {
	cli  *cli.Config
	slug string
	args []string
}

// New returns a new execute cobra command.
func New(c *cli.Config) *cobra.Command {
	var cfg = config{cli: c}

	cmd := &cobra.Command{
		Use:     "execute <slug>",
		Short:   "Execute a task",
		Long:    "Execute a task by its slug with the provided arguments.",
		Example: "airplane execute echo -- --name value",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg.slug = args[0]
			cfg.args = args[1:]
			return run(cmd.Context(), cfg)
		},
	}

	return cmd
}

// Run runs the execute command.
func run(ctx context.Context, cfg config) error {
	var client = cfg.cli.Client

	task, err := client.GetTask(ctx, cfg.slug)
	if err != nil {
		return errors.Wrap(err, "get task")
	}

	req := api.RunTaskRequest{
		TaskID:     task.ID,
		Parameters: make(api.Values),
	}
	set := flagset(task, req.Parameters)

	if err := set.Parse(cfg.args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	fmt.Printf("Running: %s\n", task.Name)

	run, err := client.RunTask(ctx, req)
	if err != nil {
		return err
	}

	fmt.Printf("Queued: %s\n", client.RunURL(run.RunID))

	var resp api.GetRunResponse

	for {
		resp, err = client.GetRun(ctx, run.RunID)
		if err != nil {
			return errors.Wrap(err, "get run")
		}

		var done bool

		switch resp.Run.Status {
		case api.RunSucceeded,
			api.RunCancelled,
			api.RunFailed:
			done = true
		}

		if done {
			break
		}

		time.Sleep(1 * time.Second)
	}

	fmt.Printf("Done: %s\n", resp.Run.Status)
	return nil
}

// Flagset returns a new flagset from the given task parameters.
func flagset(task api.Task, args api.Values) *flag.FlagSet {
	var set = flag.NewFlagSet(task.Name, flag.ContinueOnError)

	set.Usage = func() {
		fmt.Printf("\n%s Usage:\n", task.Name)
		set.VisitAll(func(f *flag.Flag) {
			fmt.Printf("  --%s %s (default: %q)\n", f.Name, f.Usage, f.DefValue)
		})
		fmt.Println()
	}

	for _, p := range task.Parameters {
		var slug = p.Slug
		var typ = p.Type
		var def = p.Default

		set.Func(p.Slug, p.Desc, func(v string) (err error) {
			if v == "" {
				args[slug] = def
				return nil
			}

			switch typ {
			case api.TypeString:
				args[slug] = v

			case api.TypeBoolean:
				b, err := strconv.ParseBool(v)
				if err != nil {
					return err
				}
				args[slug] = b

			case api.TypeInteger:
				n, err := strconv.Atoi(v)
				if err != nil {
					return err
				}
				args[slug] = n

			case api.TypeFloat:
				n, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return err
				}
				args[slug] = n

			case api.TypeUpload:
				// TODO(amir): we need to support them with some special
				// character perhaps `@` like curl?
				return errors.New("uploads are not supported from the CLI")

			case api.TypeDate:
				args[slug] = v

			case api.TypeDatetime:
				args[slug] = v
			}

			return nil
		})
	}

	return set
}