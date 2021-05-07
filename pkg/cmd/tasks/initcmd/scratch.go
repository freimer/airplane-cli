package initcmd

import (
	"context"
	"io/ioutil"
	"os"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/airplanedev/cli/pkg/cmd/tasks/initcmd/scaffolders"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/taskdir"
	"github.com/airplanedev/cli/pkg/taskdir/definitions"
	"github.com/pkg/errors"
)

func initFromScratch(ctx context.Context, cfg config) error {
	client := cfg.root.Client

	runtime, err := pickRuntime()
	if err != nil {
		return err
	}

	name, err := pickString("Enter a name:", survey.WithValidator(survey.Required))
	if err != nil {
		return err
	}

	description, err := pickString("Enter a description (optional):")
	if err != nil {
		return err
	}

	file := cfg.file
	if file == "" {
		file = "airplane.yml"
	}

	dir, err := taskdir.New(file)
	if err != nil {
		return err
	}
	defer dir.Close()

	r, err := client.GetUniqueSlug(ctx, name, "")
	if err != nil {
		return errors.Wrap(err, "getting unique slug")
	}

	def := definitions.Definition{
		Slug:        r.Slug,
		Name:        name,
		Description: description,
	}

	var scaffolder scaffolders.RuntimeScaffolder
	if runtime == runtimeKindManual {
		// TODO: let folks enter an image
		manual := definitions.ManualDefinition{
			Image:   "alpine:3",
			Command: []string{"echo", `"Hello World"`},
		}
		def.Manual = &manual
	} else {
		if scaffolder, err = defaultRuntimeConfig(runtime, &def); err != nil {
			return err
		}
	}

	if err := dir.WriteDefinition(def); err != nil {
		return err
	}

	var fileNames []string
	if scaffolder != nil {
		fileNames, err = writeRuntimeFiles(def, scaffolder)
		if err != nil {
			return err
		}
	}

	fileNames = append([]string{file}, fileNames...)
	fileNameList := "  - " + strings.Join(fileNames, "\n  - ")
	logger.Log(`
A skeleton Airplane task definition for '%s' has been created, along with other starter files:
%s

Once you are ready, deploy it to Airplane with:
  airplane deploy -f %s`, name, fileNameList, file)

	return nil
}

func defaultRuntimeConfig(runtime runtimeKind, def *definitions.Definition) (scaffolders.RuntimeScaffolder, error) {
	// TODO: let folks configure the following configuration
	switch runtime {
	case runtimeKindDeno:
		def.Deno = &definitions.DenoDefinition{
			Entrypoint: "main.ts",
		}
		return scaffolders.DenoScaffolder{Entrypoint: "main.ts"}, nil
	case runtimeKindDockerfile:
		def.Dockerfile = &definitions.DockerDefinition{
			Dockerfile: "Dockerfile",
		}
		return scaffolders.DockerfileScaffolder{Dockerfile: "Dockerfile"}, nil
	case runtimeKindGo:
		def.Go = &definitions.GoDefinition{
			Entrypoint: "main.go",
		}
		return scaffolders.GoScaffolder{Entrypoint: "main.go"}, nil
	case runtimeKindNode:
		def.Node = &definitions.NodeDefinition{
			Entrypoint:  "main.js",
			Language:    "javascript",
			NodeVersion: "15",
		}
		return scaffolders.NodeScaffolder{Entrypoint: "main.js"}, nil
	case runtimeKindPython:
		def.Python = &definitions.PythonDefinition{
			Entrypoint: "main.py",
		}
		return scaffolders.PythonScaffolder{Entrypoint: "main.py"}, nil
	default:
		return nil, errors.Errorf("unknown runtime: %s", runtime)
	}
}

type runtimeKind string

const (
	runtimeKindNode       runtimeKind = "Node.js"
	runtimeKindPython     runtimeKind = "Python"
	runtimeKindDeno       runtimeKind = "Deno"
	runtimeKindDockerfile runtimeKind = "Dockerfile"
	runtimeKindGo         runtimeKind = "Go"
	runtimeKindManual     runtimeKind = "Pre-built Docker image"
)

func pickRuntime() (runtimeKind, error) {
	var runtime string
	if err := survey.AskOne(
		&survey.Select{
			Message: "Pick a runtime:",
			Options: []string{
				string(runtimeKindNode),
				string(runtimeKindPython),
				string(runtimeKindDeno),
				string(runtimeKindDockerfile),
				string(runtimeKindGo),
				string(runtimeKindManual),
			},
			Default: string(runtimeKindNode),
		},
		&runtime,
		survey.WithStdio(os.Stdin, os.Stderr, os.Stderr),
	); err != nil {
		return runtimeKind(""), errors.Wrap(err, "selecting runtime")
	}

	return runtimeKind(runtime), nil
}

func pickString(msg string, opts ...survey.AskOpt) (string, error) {
	var str string
	opts = append(opts, survey.WithStdio(os.Stdin, os.Stderr, os.Stderr))
	if err := survey.AskOne(
		&survey.Input{
			Message: msg,
		},
		&str,
		opts...,
	); err != nil {
		return "", errors.Wrap(err, "prompting")
	}

	return str, nil
}

// For the various runtimes, we pre-populate basic versions of e.g. package.json to reduce how much
// the user has to set up.
func writeRuntimeFiles(def definitions.Definition, scaffolder scaffolders.RuntimeScaffolder) ([]string, error) {
	fileNames := []string{}
	files := map[string][]byte{}
	if err := scaffolder.GenerateFiles(def, files); err != nil {
		return nil, err
	}
	for filePath, fileContents := range files {
		logger.Debug("writing file %s", filePath)
		if err := ioutil.WriteFile(filePath, fileContents, 0664); err != nil {
			return nil, errors.Wrapf(err, "writing %s", filePath)
		}
		fileNames = append(fileNames, filePath)
	}
	return fileNames, nil
}
