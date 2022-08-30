package initcmd

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/MakeNowJust/heredoc"
	"github.com/airplanedev/cli/cmd/airplane/auth/login"
	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/cli"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/utils"
	"github.com/airplanedev/lib/pkg/build"
	"github.com/airplanedev/lib/pkg/deploy/taskdir/definitions"
	"github.com/airplanedev/lib/pkg/utils/fsx"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

type config struct {
	client         *api.Client
	envSlug        string
	name           string
	viewDir        string
	slug           string
	from           string
	gettingStarted bool
}

const gettingStartedExample = "github.com/airplanedev/examples/views/getting_started"

func New(c *cli.Config) *cobra.Command {
	var cfg = GetConfig(c.Client)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a view definition",
		Example: heredoc.Doc(fmt.Sprintf(`
			$ airplane views init
			$ airplane views init --from %s
		`, gettingStartedExample)),
		// TODO: support passing in where to create the directory either as arg or flag
		Args: cobra.MaximumNArgs(0),
		PersistentPreRunE: utils.WithParentPersistentPreRunE(func(cmd *cobra.Command, args []string) error {
			return login.EnsureLoggedIn(cmd.Root().Context(), c)
		}),
		RunE: func(cmd *cobra.Command, args []string) error {
			return Run(cmd.Root().Context(), cfg)
		},
	}
	cmd.Flags().StringVar(&cfg.envSlug, "env", "", "The slug of the environment to query. Defaults to your team's default environment.")
	cmd.Flags().StringVar(&cfg.from, "from", "", "Path to an existing github folder to initialize.")
	cmd.Flags().BoolVar(&cfg.gettingStarted, "getting-started", false, "True to generate starter tasks and views.")

	return cmd
}

func GetConfig(client *api.Client) config {
	return config{client: client}
}

func Run(ctx context.Context, cfg config) error {
	if cfg.gettingStarted {
		_, err := createDemoDB(ctx, cfg)
		if err != nil {
			return err
		}
		if err := utils.CopyFromGithubPath(gettingStartedExample); err != nil {
			return err
		}
		(&cfg).viewDir = filepath.Base(gettingStartedExample)
	} else if cfg.from != "" {
		if err := utils.CopyFromGithubPath(cfg.from); err != nil {
			return err
		}
		(&cfg).viewDir = filepath.Base(cfg.from)
	} else {
		if err := promptForNewView(&cfg); err != nil {
			return err
		}
		if err := createViewScaffolding(ctx, &cfg); err != nil {
			return err
		}
	}
	suggestNextSteps(cfg)
	return nil
}

func promptForNewView(config *config) error {
	if err := survey.AskOne(
		&survey.Input{
			Message: "What should this view be called?",
		},
		&config.name,
	); err != nil {
		return err
	}
	return nil
}

func createViewScaffolding(ctx context.Context, cfg *config) error {
	slug := utils.MakeSlug(cfg.name)
	cfg.slug = slug

	// Default to creating folder with the slug
	directory := slug
	if err := utils.CreateDirectory(directory); err != nil {
		return err
	}
	cfg.viewDir = directory

	// TODO: Add the views scaffolding files to directory
	if err := createViewDefinition(*cfg); err != nil {
		return err
	}
	if err := createEntrypoint(*cfg); err != nil {
		return err
	}
	if err := createTSConfigFile(); err != nil {
		return err
	}
	if err := createPackageJSON(*cfg); err != nil {
		return err
	}
	return nil
}

func generateEntrypointPath(cfg config, inViewDir bool) string {
	if inViewDir {
		return fmt.Sprintf("%s.view.tsx", cfg.slug)
	} else {
		return fmt.Sprintf("%s/%s.view.tsx", cfg.viewDir, cfg.slug)
	}
}

func generateDefinitionFilePath(cfg config) string {
	return fmt.Sprintf("%s/%s.view.yaml", cfg.viewDir, cfg.slug)
}

func createDemoDB(ctx context.Context, cfg config) (string, error) {
	demoDBName := "[Demo DB]"
	return cfg.client.CreateDemoDB(ctx, demoDBName)
}

func createViewDefinition(cfg config) error {
	if cfg.name == "" {
		return errors.New("missing new view name")
	}

	def := definitions.ViewDefinition{
		Name:       cfg.name,
		Slug:       cfg.slug,
		Entrypoint: generateEntrypointPath(cfg, true),
	}

	defnFilename := generateDefinitionFilePath(cfg)

	buf, err := def.GenerateCommentedFile()
	if err != nil {
		return err
	}

	if err := ioutil.WriteFile(defnFilename, buf, 0644); err != nil {
		return err
	}
	logger.Step("Created view definition at %s", defnFilename)
	return nil
}

//go:embed scaffolding/default.view.tsx
var defaultEntrypoint []byte

func createEntrypoint(cfg config) error {
	entrypointPath := generateEntrypointPath(cfg, false)

	if err := ioutil.WriteFile(entrypointPath, defaultEntrypoint, 0644); err != nil {
		return errors.Wrap(err, "creating view entrypoint")
	}
	logger.Step("Created view entrypoint at %s", entrypointPath)
	return nil
}

// createPackageJSON ensures there is a package.json in the cwd or a parent directory with the views dependencies installed.
// If package.json exists in cwd, use it.
// If package.json exists in parent directory, ask user if they want to use that or create a new one.
// If package.json doesn't exist, create a new one.
// TODO: support entrypoint override
func createPackageJSON(cfg config) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	absEntrypointPath, err := filepath.Abs(filepath.Join(cwd, generateEntrypointPath(cfg, false)))
	if err != nil {
		return err
	}
	// Check if there's a package.json in the current or parent directory of entrypoint
	packageJSONDirPath, ok := fsx.Find(absEntrypointPath, "package.json")
	useYarn := utils.ShouldUseYarn(packageJSONDirPath)

	if ok {
		if packageJSONDirPath == cwd {
			return addAllPackages(packageJSONDirPath, useYarn)
		}
		opts := []string{
			"Yes",
			"No, create package.json in my working directory",
		}
		useExisting := opts[0]
		var surveyResp string
		if err := survey.AskOne(
			&survey.Select{
				Message: fmt.Sprintf("Found existing package.json in %s. Use this to manage dependencies for your view?", packageJSONDirPath),
				Options: opts,
				Default: useExisting,
			},
			&surveyResp,
		); err != nil {
			return err
		}
		if surveyResp == useExisting {
			return addAllPackages(packageJSONDirPath, useYarn)
		}
	}

	if err := createPackageJSONFile(cwd); err != nil {
		return err
	}
	return addAllPackages(cwd, useYarn)
}

func addAllPackages(packageJSONDirPath string, useYarn bool) error {
	l := logger.NewStdErrLogger(logger.StdErrLoggerOpts{WithLoader: true})
	defer l.StopLoader()
	packageJSONPath := filepath.Join(packageJSONDirPath, "package.json")
	existingDeps, err := build.ListDependencies(packageJSONPath)
	if err != nil {
		return err
	}

	existingDepNames := make([]string, 0, len(existingDeps))
	for dep := range existingDeps {
		existingDepNames = append(existingDepNames, dep)
	}

	// TODO: Select versions to install instead of installing latest.
	// Put these in lib and use same ones for airplane views dev.
	packagesToCheck := []string{"@airplane/views", "react", "react-dom"}
	packagesToAdd := getPackagesToAdd(packagesToCheck, existingDepNames)

	devPackagesToCheck := []string{"@types/react", "@types/react-dom", "typescript"}
	devPackagesToAdd := getPackagesToAdd(devPackagesToCheck, existingDepNames)

	if len(packagesToAdd) > 0 || len(devPackagesToAdd) > 0 {
		l.Step("Installing dependencies...")
	}

	if len(packagesToAdd) > 0 {
		if err := addPackages(l, packageJSONDirPath, packagesToAdd, false, useYarn); err != nil {
			return errors.Wrap(err, "installing dependencies")
		}
	}

	if len(devPackagesToAdd) > 0 {
		if err := addPackages(l, packageJSONDirPath, devPackagesToAdd, true, useYarn); err != nil {
			return errors.Wrap(err, "installing dev dependencies")
		}
	}
	return nil
}

func getPackagesToAdd(packagesToCheck, existingDeps []string) []string {
	packagesToAdd := []string{}
	for _, pkg := range packagesToCheck {
		hasPackage := false
		for _, d := range existingDeps {
			if d == pkg {
				hasPackage = true
				break
			}
		}
		if !hasPackage {
			packagesToAdd = append(packagesToAdd, pkg)
		}
	}
	return packagesToAdd
}

func addPackages(l logger.Logger, packageJSONDirPath string, packageNames []string, dev, useYarn bool) error {
	installArgs := []string{"add"}
	if dev {
		if useYarn {
			installArgs = append(installArgs, "--dev")
		} else {
			installArgs = append(installArgs, "--save-dev")
		}
	}
	installArgs = append(installArgs, packageNames...)
	var cmd *exec.Cmd
	if useYarn {
		cmd = exec.Command("yarn", installArgs...)
		l.Debug("Adding packages using yarn")
	} else {
		cmd = exec.Command("npm", installArgs...)
		l.Debug("Adding packages using npm")
	}

	cmd.Dir = packageJSONDirPath
	err := cmd.Run()
	if err != nil {
		if dev {
			l.Log("Failed to install devDependencies")
		} else {
			l.Log("Failed to install dependencies")
		}
		return err
	}
	for _, pkg := range packageNames {
		l.Step(fmt.Sprintf("Installed %s", pkg))
	}
	return nil
}

//go:embed scaffolding/package.json
var packageJsonTemplateStr string

func createPackageJSONFile(cwd string) error {
	tmpl, err := template.New("packageJson").Parse(packageJsonTemplateStr)
	if err != nil {
		return errors.Wrap(err, "parsing package.json template")
	}
	normalizedCwd := strings.ReplaceAll(strings.ToLower(filepath.Base(cwd)), " ", "-")
	buf := new(bytes.Buffer)
	if err := tmpl.Execute(buf, map[string]interface{}{
		"name": normalizedCwd,
	}); err != nil {
		return errors.Wrap(err, "executing package.json template")
	}

	if err := ioutil.WriteFile("package.json", buf.Bytes(), 0644); err != nil {
		return errors.Wrap(err, "writing package.json")
	}
	logger.Step("Created package.json")
	return nil
}

//go:embed scaffolding/tsconfig.json
var defaultTSConfig []byte

func createTSConfigFile() error {
	if !fsx.Exists("tsconfig.json") {
		if err := ioutil.WriteFile("tsconfig.json", defaultTSConfig, 0644); err != nil {
			return errors.Wrap(err, "creating tsconfig.json")
		}
		logger.Step("Created tsconfig.json")
	}
	return nil
}

func suggestNextSteps(cfg config) {
	if cfg.viewDir != "" && cfg.slug != "" {
		logger.Suggest("✅ To complete your view:", fmt.Sprintf("Write your view logic in %s", generateEntrypointPath(cfg, false)))
	}

	logger.Suggest(
		"⚡ To develop your view locally:",
		"airplane views dev %s",
		cfg.viewDir,
	)

	var deployDir string
	if cfg.viewDir != "" {
		deployDir = cfg.viewDir
	} else {
		deployDir = "."
	}
	logger.Suggest(
		"🛫 To deploy your view to Airplane:",
		"airplane deploy %s",
		deployDir,
	)
}
