// initcmd defines the implementation of the `airplane tasks init` command.
//
// Even though the command is called "init", we can't name the package "init"
// since that conflicts with the Go init function.
package initcmd

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/MakeNowJust/heredoc"
	"github.com/airplanedev/cli/cmd/airplane/auth/login"
	"github.com/airplanedev/cli/pkg/api"
	"github.com/airplanedev/cli/pkg/cli"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/node"
	"github.com/airplanedev/cli/pkg/utils"
	libapi "github.com/airplanedev/lib/pkg/api"
	"github.com/airplanedev/lib/pkg/build"
	"github.com/airplanedev/lib/pkg/deploy/taskdir/definitions"
	"github.com/airplanedev/lib/pkg/runtime"
	_ "github.com/airplanedev/lib/pkg/runtime/javascript"
	_ "github.com/airplanedev/lib/pkg/runtime/python"
	_ "github.com/airplanedev/lib/pkg/runtime/rest"
	_ "github.com/airplanedev/lib/pkg/runtime/shell"
	_ "github.com/airplanedev/lib/pkg/runtime/sql"
	_ "github.com/airplanedev/lib/pkg/runtime/typescript"
	"github.com/airplanedev/lib/pkg/utils/fsx"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"golang.org/x/exp/slices"
)

type config struct {
	client *api.Client
	file   string
	from   string

	codeOnly  bool
	assumeYes bool
	assumeNo  bool
	envSlug   string

	inline   bool
	workflow bool

	newTaskInfo newTaskInfo
}

type newTaskInfo struct {
	name       string
	kind       build.TaskKind
	entrypoint string
}

func New(c *cli.Config) *cobra.Command {
	var cfg = GetConfig(c.Client)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a task definition",
		Example: heredoc.Doc(`
			$ airplane tasks init --from task_slug
			$ airplane tasks init --from task_slug ./folder/my_task.js
			$ airplane tasks init --from task_slug ./folder/my_task.task.json
			$ airplane tasks init --from task_slug ./folder/my_task.task.yaml
			$ airplane tasks init --from github.com/airplanedev/examples/node/hello-world-javascript/node_hello_world_js.task.yaml
		`),
		Args: cobra.MaximumNArgs(1),
		PersistentPreRunE: utils.WithParentPersistentPreRunE(func(cmd *cobra.Command, args []string) error {
			return login.EnsureLoggedIn(cmd.Root().Context(), c)
		}),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				cfg.file = args[0]
			}
			return Run(cmd.Root().Context(), cfg)
		},
	}

	cmd.Flags().StringVar(&cfg.from, "slug", "", "Slug of an existing task to generate from.")

	cmd.Flags().StringVar(&cfg.from, "from", "", "Slug of an existing task to initialize.")
	cmd.Flags().BoolVar(&cfg.codeOnly, "code-only", false, "True to skip creating a task definition file; only generates an entrypoint file.")
	cmd.Flags().BoolVarP(&cfg.assumeYes, "yes", "y", false, "True to specify automatic yes to prompts.")
	cmd.Flags().BoolVarP(&cfg.assumeNo, "no", "n", false, "True to specify automatic no to prompts.")

	cmd.Flags().BoolVar(&cfg.inline, "inline", false, "Generate inline config for custom tasks")
	cmd.Flags().BoolVar(&cfg.workflow, "workflow", false, "Generate a workflow instead of a task. Implies --inline.")

	if err := cmd.Flags().MarkHidden("slug"); err != nil {
		logger.Debug("error: %s", err)
	}
	if err := cmd.Flags().MarkHidden("inline"); err != nil {
		logger.Debug("error: %s", err)
	}

	// Unhide this flag once we release environments.
	cmd.Flags().StringVar(&cfg.envSlug, "env", "", "The slug of the environment to query. Defaults to your team's default environment.")

	return cmd
}

func GetConfig(client *api.Client) config {
	return config{client: client}
}

func Run(ctx context.Context, cfg config) error {
	// Check for mutually exclusive flags.
	if cfg.assumeYes && cfg.assumeNo {
		return errors.New("Cannot specify both --yes and --no")
	}
	if cfg.codeOnly && cfg.from == "" {
		return errors.New("Required flag(s) \"from\" not set")
	}

	// workflows are also inline
	cfg.inline = cfg.inline || cfg.workflow

	if cfg.codeOnly && cfg.inline {
		return errors.New("Cannot specify both --code-only and --inline")
	}

	if strings.HasPrefix(cfg.from, "github.com/") || strings.HasPrefix(cfg.from, "https://github.com/") {
		return initWithExample(ctx, cfg)
	}

	if cfg.from == "" {
		// Prompt for new task information.
		if err := promptForNewTask(cfg.file, &cfg.newTaskInfo, cfg.inline, cfg.workflow); err != nil {
			return err
		}
	}

	if cfg.codeOnly {
		return initCodeOnly(ctx, cfg)
	}

	return initWithTaskDef(ctx, cfg)
}

func initWithTaskDef(ctx context.Context, cfg config) error {
	client := cfg.client

	// workflows are also inline
	cfg.inline = cfg.inline || cfg.workflow
	var def definitions.Definition_0_3
	if cfg.from != "" {
		task, err := client.GetTask(ctx, libapi.GetTaskRequest{
			Slug:    cfg.from,
			EnvSlug: cfg.envSlug,
		})
		if err != nil {
			return err
		}

		if task.Runtime == build.TaskRuntimeWorkflow {
			cfg.workflow = true
			cfg.inline = true
		}

		if cfg.inline && !isInlineSupportedKind(task.Kind) {
			return errors.New("Inline config is only supported for Node tasks.")
		}

		def, err = definitions.NewDefinitionFromTask_0_3(ctx, cfg.client, task)
		if err != nil {
			return err
		}
	} else {
		if cfg.newTaskInfo.name == "" || cfg.newTaskInfo.kind == "" {
			return errors.New("missing new task info")
		}

		var err error
		def, err = definitions.NewDefinition_0_3(cfg.newTaskInfo.name,
			utils.MakeSlug(cfg.newTaskInfo.name), cfg.newTaskInfo.kind, cfg.newTaskInfo.entrypoint)
		if err != nil {
			return err
		}
	}

	kind, err := def.Kind()
	if err != nil {
		return err
	}

	if cfg.workflow {
		def.Runtime = build.TaskRuntimeWorkflow
	}

	localExecutionSupported := false
	if entrypoint, err := def.Entrypoint(); err == definitions.ErrNoEntrypoint {
		// no-op
	} else if err != nil {
		return err
	} else {
		if cfg.file != "" && !definitions.IsTaskDef(cfg.file) {
			entrypoint = cfg.file
		}

		entrypointBase := path.Base(entrypoint)
		if strings.HasSuffix(entrypointBase, ".view.tsx") || strings.HasSuffix(entrypointBase, ".view.jsx") {
			logger.Log("Task %s was deployed from the view file %s.", def.GetSlug(), entrypointBase)
			logger.Log("You should run `airplane views init` if you'd like to initialize this task.")
			if ok, err := utils.ConfirmWithAssumptions("Are you sure you'd like to continue?", cfg.assumeYes, cfg.assumeNo); err != nil {
				return err
			} else if !ok {
				logger.Log("Exiting flow")
				return nil
			}
		}

		for {

			if cfg.assumeYes && cfg.file != "" {
				entrypoint = cfg.file
			} else {
				entrypoint, err = promptForEntrypoint(def.GetSlug(), kind, entrypoint, cfg)
				if err != nil {
					return err
				}
			}

			if fsx.Exists(entrypoint) {
				var question string
				if !cfg.inline {
					question = fmt.Sprintf("Are you sure you want to link %s? You should only link existing Airplane scripts.", entrypoint)
					if kind == build.TaskKindSQL {
						question = fmt.Sprintf("Would you like to overwrite %s?", entrypoint)
					}
				} else {
					question = fmt.Sprintf("Would you like to overwrite %s?", entrypoint)
				}
				if ok, err := utils.ConfirmWithAssumptions(question, cfg.assumeYes, cfg.assumeNo); err != nil {
					return err
				} else if ok {
					break
				}
			} else {
				break
			}
		}

		if err := def.SetEntrypoint(entrypoint); err != nil {
			return err
		}

		r, err := runtime.Lookup(entrypoint, kind)
		if err != nil {
			return errors.Wrapf(err, "unable to init %q - check that your CLI is up to date", entrypoint)
		}
		localExecutionSupported = r.SupportsLocalExecution()

		if kind == build.TaskKindSQL {
			query, err := def.SQL.GetQuery()
			if err != nil {
				// Create a generic entrypoint.
				if err := createEntrypoint(r, entrypoint, nil); err != nil {
					return errors.Wrapf(err, "unable to create entrypoint")
				}
			} else {
				// Write the query to the entrypoint.
				if err := writeEntrypoint(entrypoint, []byte(query), 0644); err != nil {
					return errors.Wrapf(err, "unable to create entrypoint")
				}
			}
			logger.Step("Created %s", entrypoint)
		} else if cfg.inline {
			if err := createInlineEntrypoint(r, entrypoint, &def); err != nil {
				return errors.Wrapf(err, "unable to create entrypoint")
			}
			logger.Step("Created %s", entrypoint)
		} else {
			// Create entrypoint, without comment link, if it doesn't exist.
			if !fsx.Exists(entrypoint) {
				if err := createEntrypoint(r, entrypoint, nil); err != nil {
					return errors.Wrapf(err, "unable to create entrypoint")
				}
				logger.Step("Created %s", entrypoint)
			}
		}
	}

	var resp *writeDefnFileResponse
	if !cfg.inline {
		resp, err = writeDefnFile(&def, cfg)
		if err != nil {
			return err
		}
		if resp == nil {
			return nil
		}
	} else {
		entrypoint, _ := def.Entrypoint()
		resp = &writeDefnFileResponse{
			DefnFile:       entrypoint,
			EntrypointFile: entrypoint,
		}
	}

	if err := runKindSpecificInstallation(kind); err != nil {
		return err
	}

	suggestNextSteps(suggestNextStepsRequest{
		defnFile:           resp.DefnFile,
		entrypoint:         resp.EntrypointFile,
		showLocalExecution: localExecutionSupported,
		kind:               kind,
		isNew:              cfg.from == "",
	})
	return nil
}

type writeDefnFileResponse struct {
	DefnFile       string
	EntrypointFile string
}

func writeDefnFile(def *definitions.Definition_0_3, cfg config) (*writeDefnFileResponse, error) {
	// Create task defn file.
	defnFilename := cfg.file
	if !definitions.IsTaskDef(cfg.file) {
		defaultDefnFn := fmt.Sprintf("%s.task.yaml", def.Slug)
		entrypoint, _ := def.Entrypoint()
		fn, err := promptForNewDefinition(defaultDefnFn, entrypoint)
		if err != nil {
			return nil, err
		}
		defnFilename = fn
	}
	if fsx.Exists(defnFilename) {
		// If it exists, check for existence of this file before overwriting it.
		question := fmt.Sprintf("Would you like to overwrite %s?", defnFilename)
		if ok, err := utils.ConfirmWithAssumptions(question, cfg.assumeYes, cfg.assumeNo); err != nil {
			return nil, err
		} else if !ok {
			// User answered "no", so bail here.
			return nil, nil
		}
	}

	// Adjust entrypoint to be relative to the task defn.
	entrypoint, err := def.Entrypoint()
	if err == definitions.ErrNoEntrypoint {
		// no-op
	} else if err != nil {
		return nil, err
	} else {
		absEntrypoint, err := filepath.Abs(entrypoint)
		if err != nil {
			return nil, errors.Wrap(err, "determining absolute entrypoint")
		}

		absDefnFn, err := filepath.Abs(defnFilename)
		if err != nil {
			return nil, errors.Wrap(err, "determining absolute definition file")
		}

		defnDir := filepath.Dir(absDefnFn)
		relpath, err := filepath.Rel(defnDir, absEntrypoint)
		if err != nil {
			return nil, errors.Wrap(err, "determining relative entrypoint")
		}
		if err := def.SetEntrypoint(relpath); err != nil {
			return nil, err
		}
	}

	buf, err := def.GenerateCommentedFile(definitions.GetTaskDefFormat(defnFilename))
	if err != nil {
		return nil, err
	}

	if err := ioutil.WriteFile(defnFilename, buf, 0644); err != nil {
		return nil, err
	}
	logger.Step("Created %s", defnFilename)
	return &writeDefnFileResponse{
		DefnFile:       defnFilename,
		EntrypointFile: entrypoint,
	}, nil
}

func initCodeOnly(ctx context.Context, cfg config) error {
	client := cfg.client

	task, err := client.GetTask(ctx, libapi.GetTaskRequest{
		Slug:    cfg.from,
		EnvSlug: cfg.envSlug,
	})
	if err != nil {
		return err
	}

	if cfg.file == "" {
		cfg.file, err = promptForEntrypoint(task.Slug, task.Kind, "", cfg)
		if err != nil {
			return err
		}
	}

	r, err := runtime.Lookup(cfg.file, task.Kind)
	if err != nil {
		return errors.Wrapf(err, "unable to init %q - check that your CLI is up to date", cfg.file)
	}

	if fsx.Exists(cfg.file) {
		if slug := runtime.Slug(cfg.file); slug == task.Slug {
			logger.Step("%s is already linked to %s", cfg.file, cfg.from)
			suggestNextSteps(suggestNextStepsRequest{
				entrypoint:         cfg.file,
				showLocalExecution: true,
				kind:               task.Kind,
			})
			return nil
		}

		patch, err := patch(cfg.from, cfg.file)
		if err != nil {
			return err
		}

		if !patch {
			logger.Log("You canceled linking %s to %s", cfg.file, cfg.from)
			return nil
		}

		buf, err := ioutil.ReadFile(cfg.file)
		if err != nil {
			return err
		}
		code := prependComment(buf, runtime.Comment(r, task.URL))
		// Note: 0644 is ignored because file already exists. Uses a reasonable default just in case.
		if err := ioutil.WriteFile(cfg.file, code, 0644); err != nil {
			return err
		}
		logger.Step("Linked %s to %s", cfg.file, cfg.from)

		suggestNextSteps(suggestNextStepsRequest{
			entrypoint:         cfg.file,
			showLocalExecution: true,
			kind:               task.Kind,
		})
		return nil
	}

	if err := createEntrypoint(r, cfg.file, &task); err != nil {
		return err
	}
	logger.Step("Created %s", cfg.file)
	suggestNextSteps(suggestNextStepsRequest{
		entrypoint:         cfg.file,
		showLocalExecution: true,
		kind:               task.Kind,
	})
	return nil
}

// prependComment handles writing the linking comment to source code, accounting for shebangs
// (which have to appear first in the file).
func prependComment(source []byte, comment string) []byte {
	var buf bytes.Buffer

	// Regardless of task type, look for a shebang and put comment after it if detected.
	hasShebang := len(source) >= 2 && source[0] == '#' && source[1] == '!'
	appendAfterFirstNewline := hasShebang

	appendComment := func() {
		buf.WriteString(comment)
		buf.WriteRune('\n')
		buf.WriteRune('\n')
	}

	prepended := false
	if !appendAfterFirstNewline {
		appendComment()
		prepended = true
	}
	for _, char := range string(source) {
		buf.WriteRune(char)
		if char == '\n' && appendAfterFirstNewline && !prepended {
			appendComment()
			prepended = true
		}
	}
	return buf.Bytes()
}

type suggestNextStepsRequest struct {
	defnFile           string
	entrypoint         string
	showLocalExecution bool
	kind               build.TaskKind
	isNew              bool
}

func suggestNextSteps(req suggestNextStepsRequest) {
	// Update the next steps for inline code config
	if req.isNew {
		steps := []string{}
		switch req.kind {
		case build.TaskKindSQL:
			steps = append(steps, fmt.Sprintf("Add the name of a database resource to %s", req.defnFile))
			steps = append(steps, fmt.Sprintf("Write your query in %s", req.entrypoint))
		case build.TaskKindREST:
			steps = append(steps, fmt.Sprintf("Add the name of a REST resource to %s", req.defnFile))
			steps = append(steps, fmt.Sprintf("Specify the details of your REST request in %s", req.defnFile))
		case build.TaskKindImage:
			steps = append(steps, fmt.Sprintf("Add the name of a Docker image to %s", req.defnFile))
		default:
			steps = append(steps, fmt.Sprintf("Write your task logic in %s", req.entrypoint))
		}
		if req.defnFile != "" {
			steps = append(steps, fmt.Sprintf("Add a description, parameters, and more details in %s", req.defnFile))
		}
		logger.SuggestSteps("✅ To complete your task:", steps...)
	}

	file := req.defnFile
	if req.defnFile == "" {
		file = req.entrypoint
	}
	if req.showLocalExecution {
		logger.Suggest(
			"⚡ To execute the task locally:",
			"airplane dev %s",
			file,
		)
	}
	logger.Suggest(
		"🛫 To deploy your task to Airplane:",
		"airplane deploy %s",
		file,
	)
}

// Patch asks the user if he would like to patch a file
// and add the airplane special comment.
func patch(slug, file string) (ok bool, err error) {
	err = survey.AskOne(
		&survey.Confirm{
			Message: fmt.Sprintf("Would you like to link %s to %s?", file, slug),
			Help:    "Linking this file will add a special airplane comment.",
			Default: true,
		},
		&ok,
	)
	return
}

func promptForEntrypoint(slug string, kind build.TaskKind, defaultEntrypoint string, cfg config) (string, error) {
	exts := runtime.SuggestExts(kind)
	if defaultEntrypoint == "" {
		defaultEntrypoint = slug
		if kind == build.TaskKindNode && len(exts) > 1 {
			// Special case node tasks and make their extensions '.ts'
			defaultEntrypoint += ".ts"
		} else {
			defaultEntrypoint += exts[0]
		}

		if cwdIsHome, err := cwdIsHome(); err != nil {
			return "", err
		} else if cwdIsHome {
			// Suggest a subdirectory to avoid putting a file directly into home directory.
			defaultEntrypoint = filepath.Join("airplane", defaultEntrypoint)
		}
	}

	if !cfg.codeOnly && cfg.inline {
		defaultEntrypoint = modifyEntrypointForInline(kind, defaultEntrypoint)
	}

	var entrypoint string
	if err := survey.AskOne(
		&survey.Input{
			Message: "Where is the script for this task?",
			Default: defaultEntrypoint,
			Suggest: func(toComplete string) []string {
				files, _ := filepath.Glob(toComplete + "*")
				return files
			},
		},
		&entrypoint,
		survey.WithValidator(func(val interface{}) error {
			if len(exts) == 0 {
				return nil
			}
			if str, ok := val.(string); ok {
				for _, ext := range exts {
					if strings.HasSuffix(str, ext) {
						return nil
					}
				}
				return errors.Errorf("File must have a valid extension: %s", exts)
			}
			return errors.New("expected string")
		}),
	); err != nil {
		return "", err
	}

	directory := filepath.Dir(entrypoint)
	if err := createFolder(directory); err != nil {
		return "", errors.Wrapf(err, "Error creating directory for script.")
	}

	return entrypoint, nil
}

func promptForNewDefinition(defaultFilename, entrypoint string) (string, error) {
	entrypointDir := filepath.Dir(entrypoint)
	defaultFilename = filepath.Join(entrypointDir, defaultFilename)

	var filename string
	if err := survey.AskOne(
		&survey.Input{
			Message: "Where should the definition file be created?",
			Default: defaultFilename,
			Suggest: func(toComplete string) []string {
				files, _ := filepath.Glob(toComplete + "*")
				return files
			},
		},
		&filename,
		survey.WithValidator(func(val interface{}) error {
			if str, ok := val.(string); ok {
				if definitions.IsTaskDef(str) {
					return nil
				}
				return errors.Errorf("Definition file must have extension .task.yaml or .task.json")
			}
			return errors.New("expected string")
		}),
	); err != nil {
		return "", err
	}

	directory := filepath.Dir(filename)
	if err := createFolder(directory); err != nil {
		return "", errors.Wrapf(err, "Error creating directory for definition file.")
	}
	return filename, nil
}

func createFolder(directory string) error {
	if _, err := os.Stat(directory); err != nil {
		if !os.IsNotExist(err) {
			return err
		}

		// Directory doesn't exist, make it.
		if err := os.MkdirAll(directory, 0755); err != nil {
			return err
		}
	}
	return nil
}

var allKindsByName = map[string]build.TaskKind{
	"Docker":     build.TaskKindImage,
	"Node":       build.TaskKindNode,
	"JavaScript": build.TaskKindNode,
	"Python":     build.TaskKindPython,
	"Shell":      build.TaskKindShell,
	"SQL":        build.TaskKindSQL,
	"REST":       build.TaskKindREST,
}

// Determines the set of kind names that are eligible for use as tasks.
// The ordering is important as it defines how they are shown in CLI prompts.
var supportedTaskKindNames = []string{
	"SQL",
	"REST",
	"Node",
	"Python",
	"Shell",
	"Docker",
}

// Determines the set of kind names that are eligible for use as workflows.
// The ordering is important as it defines how they are shown in CLI prompts.
var supportedWorkflowKindNames = []string{
	"JavaScript",
}

func promptForNewTask(file string, info *newTaskInfo, inline bool, workflow bool) error {
	defFormat := definitions.GetTaskDefFormat(file)
	ext := filepath.Ext(file)
	base := strings.TrimSuffix(filepath.Base(file), ext)
	if defFormat != definitions.DefFormatUnknown {
		// Trim off the .task part, too
		base = strings.TrimSuffix(base, ".task")
	}
	if base == "." {
		base = ""
	}

	// Ask for a name.
	if err := survey.AskOne(
		&survey.Input{
			Message: "What should this task be called?",
			Default: base,
		},
		&info.name,
	); err != nil {
		return err
	}

	kindNames := supportedTaskKindNames
	if workflow {
		kindNames = supportedWorkflowKindNames
	}

	// Ask for a kind.
	defaultName := kindNames[0]
	guessKind, err := runtime.SuggestKind(ext)
	if err == nil {
		for _, name := range kindNames {
			if kind := allKindsByName[name]; kind == guessKind {
				defaultName = name
				break
			}
		}
	}

	var selectedKindName string
	if err := survey.AskOne(
		&survey.Select{
			Message: "What kind of task should this be?",
			Options: kindNames,
			Default: defaultName,
		},
		&selectedKindName,
	); err != nil {
		return err
	}
	info.kind = allKindsByName[selectedKindName]
	if info.kind == "" {
		return errors.Errorf("Unknown kind selected: %q", selectedKindName)
	}
	if inline && !isInlineSupportedKind(info.kind) {
		return errors.New("Inline config is only supported for Node tasks.")
	}

	return nil
}

var inlineSupportedKinds = []build.TaskKind{build.TaskKindNode}

func isInlineSupportedKind(kind build.TaskKind) bool {
	return slices.Contains(inlineSupportedKinds, kind)
}

func cwdIsHome() (bool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return false, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	return cwd == home, nil
}

func createEntrypoint(r runtime.Interface, entrypoint string, task *libapi.Task) error {
	code, fileMode, err := r.Generate(apiTaskToRuntimeTask(task))
	if err != nil {
		return err
	}

	return writeEntrypoint(entrypoint, code, fileMode)
}

func createInlineEntrypoint(r runtime.Interface, entrypoint string, def *definitions.Definition_0_3) error {
	code, fileMode, err := r.GenerateInline(def)
	if err != nil {
		return err
	}

	return writeEntrypoint(entrypoint, code, fileMode)
}

func modifyEntrypointForInline(kind build.TaskKind, entrypoint string) string {
	if kind != build.TaskKindNode {
		return entrypoint
	}

	ext := filepath.Ext(entrypoint)
	entrypointWithoutExt := strings.TrimSuffix(entrypoint, ext)

	if !strings.HasSuffix(entrypointWithoutExt, ".airplane") {
		return fmt.Sprintf("%s.airplane%s", entrypointWithoutExt, ext)
	}
	return entrypoint
}

func writeEntrypoint(path string, b []byte, fileMode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	if err := ioutil.WriteFile(path, b, fileMode); err != nil {
		return err
	}

	return nil
}

func apiTaskToRuntimeTask(task *libapi.Task) *runtime.Task {
	if task == nil {
		return nil
	}
	t := &runtime.Task{
		URL: task.URL,
	}
	for _, p := range task.Parameters {
		t.Parameters = append(t.Parameters, runtime.Parameter{
			Name: p.Name,
			Slug: p.Slug,
			Type: runtime.Type(p.Type),
		})
	}
	return t
}

func runKindSpecificInstallation(kind build.TaskKind) error {
	switch kind {
	case build.TaskKindNode:
		cwd, err := os.Getwd()
		if err != nil {
			return errors.Wrap(err, "getting working directory")
		}
		if err := node.CreatePackageJSON(cwd, node.NodeDependencies{
			Dependencies:    []string{"airplane"},
			DevDependencies: []string{"@types/node"},
		}); err != nil {
			return err
		}

		return node.CreateTaskTSConfig()
	default:
		return nil
	}
}
