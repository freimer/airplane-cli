package initcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/MakeNowJust/heredoc"
	"github.com/airplanedev/cli/cmd/airplane/auth/login"
	taskinit "github.com/airplanedev/cli/cmd/airplane/tasks/initcmd"
	viewinit "github.com/airplanedev/cli/cmd/airplane/views/initcmd"
	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/cli"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/utils"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type config struct {
	client      *api.Client
	template    string
	resetDemoDB bool
	dev         bool
}

func New(c *cli.Config) *cobra.Command {
	var cfg = config{client: c.Client}

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a task, view, or template",
		Example: heredoc.Doc(`
			$ airplane init --template getting_started
			$ airplane init --template github.com/airplanedev/templates/getting_started
		`),
		PersistentPreRunE: utils.WithParentPersistentPreRunE(func(cmd *cobra.Command, args []string) error {
			cfg.dev = c.Dev
			return login.EnsureLoggedIn(cmd.Root().Context(), c)
		}),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Root().Context(), cfg)
		},
		Hidden: true,
	}

	cmd.Flags().StringVarP(&cfg.template, "template", "t", "", "Path of a template to initialize from in the format github.com/org/repo/path/to/template or path/to/template (in the airplanedev/templates repository)")
	cmd.Flags().BoolVar(&cfg.resetDemoDB, "reset-demo-db", false, "Resets the SQL DB resource [Demo DB] to its original state")

	return cmd
}

const taskOption = "Task		Create a SQL query, script, or API call"
const viewOption = "View		Build a custom UI"
const templateOption = "Template	Clone ready-made tasks and views to start developing faster"

var orderedInitOptions = []string{
	taskOption,
	viewOption,
	templateOption,
}

const templateGallery = "https://docs.airplane.dev/templates"

func run(ctx context.Context, cfg config) error {
	if cfg.resetDemoDB {
		resourceID, err := cfg.client.ResetDemoDB(ctx)
		if err != nil {
			return errors.Wrap(err, "resetting demo db")
		}
		logger.Step("Demo DB reset")
		logger.Debug("Demo DB has resource ID %s", resourceID)
	}

	if cfg.template != "" {
		if strings.HasPrefix(cfg.template, "github.com/") || strings.HasPrefix(cfg.template, "https://github.com/") {
			return utils.CopyFromGithubPath(cfg.template)
		}

		templates, err := ListTemplates(ctx)
		if err != nil {
			return err
		}
		return initFromTemplate(ctx, templates, cfg.template)
	}

	if !cfg.dev {
		return taskinit.Run(ctx, taskinit.GetConfig(cfg.client))
	}

	var selectedInit string
	if err := survey.AskOne(
		&survey.Select{
			Message: "What would you like to initialize?",
			Options: orderedInitOptions,
			Default: orderedInitOptions[0],
		},
		&selectedInit,
	); err != nil {
		return err
	}
	if selectedInit == taskOption {
		return taskinit.Run(ctx, taskinit.GetConfig(cfg.client))
	} else if selectedInit == viewOption {
		return viewinit.Run(ctx, viewinit.GetConfig(cfg.client))
	} else if selectedInit == templateOption {
		templates, err := ListTemplates(ctx)
		if err != nil {
			return err
		}
		selectedTemplate, err := selectTemplate(ctx, templates)
		if err != nil {
			return err
		}

		return initFromTemplate(ctx, templates, selectedTemplate)
	}

	return nil
}

type Template struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	GitHubPath    string   `json:"githubPath"`
	DemoResources []string `json:"demoResources"`
	ViewSlugs     []string `json:"viewSlugs"`
	TaskSlugs     []string `json:"taskSlugs"`
}

func selectTemplate(ctx context.Context, templates []Template) (string, error) {
	const templateBrowser = "Explore templates in the browser"
	optionToPath := map[string]string{}

	templateShortPaths := []string{templateBrowser}
	for _, t := range templates {
		shortPath := strings.TrimPrefix(t.GitHubPath, "github.com/airplanedev/templates/")
		option := fmt.Sprintf("%s (%s)", t.Name, shortPath)
		optionToPath[option] = shortPath
		templateShortPaths = append(templateShortPaths, option)
	}
	var selectedTemplate string
	for selectedTemplate == "" || selectedTemplate == templateBrowser {
		if err := survey.AskOne(
			&survey.Select{
				Message: "Which template would you like to initialize?",
				Options: templateShortPaths,
				Default: templateShortPaths[0],
			},
			&selectedTemplate,
		); err != nil {
			return "", err
		}

		if selectedTemplate == templateBrowser {
			if ok := utils.Open(templateGallery); ok {
				logger.Log("Something went wrong with opening templates in the browser")
			}
		}
	}
	return optionToPath[selectedTemplate], nil
}

func initFromTemplate(ctx context.Context, templates []Template, gitPath string) error {
	template, err := FindTemplate(templates, gitPath)
	if err != nil {
		return err
	}
	return utils.CopyFromGithubPath(template.GitHubPath)
}

const docsUrl = "http://docs.airplane.dev/templates/templates.json"
const defaultGitPrefix = "github.com/airplanedev/templates"

func ListTemplates(ctx context.Context) ([]Template, error) {
	resp, err := http.Get(docsUrl)
	if err != nil {
		return []Template{}, errors.Wrap(err, "getting templates json")
	}
	defer resp.Body.Close()
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return []Template{}, errors.Wrap(err, "reading templates")
	}

	var t []Template
	if err = json.Unmarshal(buf, &t); err != nil {
		return []Template{}, errors.Wrap(err, "unmarshalling templates")
	}
	return t, nil
}

func FindTemplate(templates []Template, gitPath string) (Template, error) {
	if !strings.HasPrefix(gitPath, "github.com/") {
		if strings.HasPrefix(gitPath, "https://github.com/") {
			gitPath = strings.TrimPrefix(gitPath, "https://")
		} else {
			gitPath = filepath.Join(defaultGitPrefix, gitPath)
		}
	}

	for _, t := range templates {
		if t.GitHubPath == gitPath {
			return t, nil
		}
	}
	return Template{}, errors.New("template not found")
}
