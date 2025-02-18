package node

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"text/tabwriter"

	"github.com/AlecAivazis/survey/v2"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/utils"
	"github.com/airplanedev/lib/pkg/build"
	"github.com/airplanedev/lib/pkg/utils/fsx"
	"github.com/pkg/errors"
)

type NodeDependencies struct {
	Dependencies    []string
	DevDependencies []string
}

// CreatePackageJSON ensures there is a package.json in path with the provided dependencies installed.
// If package.json exists in cwd, use it.
// If package.json exists in parent directory, ask user if they want to use that or create a new one.
// If package.json doesn't exist, create a new one.
func CreatePackageJSON(directory string, dependencies NodeDependencies) error {
	// Check if there's a package.json in the current or parent directory of entrypoint
	packageJSONDirPath, ok := fsx.Find(directory, "package.json")
	useYarn := utils.ShouldUseYarn(packageJSONDirPath)

	if ok {
		if packageJSONDirPath == directory {
			return addAllPackages(packageJSONDirPath, useYarn, dependencies)
		}
		opts := []string{
			"Yes",
			"No, create package.json in my working directory",
		}
		useExisting := opts[0]
		var surveyResp string
		if err := survey.AskOne(
			&survey.Select{
				Message: fmt.Sprintf("Found existing package.json in %s. Use this to manage dependencies?", packageJSONDirPath),
				Options: opts,
				Default: useExisting,
			},
			&surveyResp,
		); err != nil {
			return err
		}
		if surveyResp == useExisting {
			return addAllPackages(packageJSONDirPath, useYarn, dependencies)
		}
	}

	if err := createPackageJSONFile(directory); err != nil {
		return err
	}
	return addAllPackages(directory, useYarn, dependencies)
}

func addAllPackages(packageJSONDirPath string, useYarn bool, dependencies NodeDependencies) error {
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
	// Put these in lib and use same ones for airplane tasks/views dev.
	packagesToAdd := getPackagesToAdd(dependencies.Dependencies, existingDepNames)
	devPackagesToAdd := getPackagesToAdd(dependencies.DevDependencies, existingDepNames)

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

	if err := os.WriteFile("package.json", buf.Bytes(), 0644); err != nil {
		return errors.Wrap(err, "writing package.json")
	}
	logger.Step("Created package.json")
	return nil
}

//go:embed scaffolding/viewTSConfig.json
var defaultViewTSConfig []byte

func CreateViewTSConfig() error {
	return mergeTSConfig(defaultViewTSConfig, MergeStrategyPreferTemplate)
}

//go:embed scaffolding/taskTSConfig.json
var defaultTaskTSConfig []byte

func CreateTaskTSConfig() error {
	return mergeTSConfig(defaultTaskTSConfig, MergeStrategyPreferExisting)
}

type MergeStrategy string

const (
	MergeStrategyPreferExisting MergeStrategy = "existing"
	MergeStrategyPreferTemplate MergeStrategy = "template"
)

func mergeTSConfig(configFile []byte, strategy MergeStrategy) error {
	if fsx.Exists("tsconfig.json") {
		templateTSConfig := map[string]interface{}{}
		err := json.Unmarshal(configFile, &templateTSConfig)
		if err != nil {
			return errors.Wrap(err, "unmarshalling tsconfig template")
		}

		logger.Step("Found existing tsconfig.json...")

		existingFile, err := os.ReadFile("tsconfig.json")
		if err != nil {
			return errors.Wrap(err, "reading existing tsconfig.json")
		}
		existingTSConfig := map[string]interface{}{}
		err = json.Unmarshal(existingFile, &existingTSConfig)
		if err != nil {
			return errors.Wrap(err, "unmarshalling existing tsconfig")
		}

		newTSConfig := map[string]interface{}{}
		if strategy == MergeStrategyPreferExisting {
			mergeTSConfigsRecursively(newTSConfig, templateTSConfig)
			mergeTSConfigsRecursively(newTSConfig, existingTSConfig)
		} else {
			mergeTSConfigsRecursively(newTSConfig, existingTSConfig)
			mergeTSConfigsRecursively(newTSConfig, templateTSConfig)
		}

		if printTSConfigChanges(newTSConfig, existingTSConfig, "") {
			var ok bool
			err = survey.AskOne(
				&survey.Confirm{
					Message: "Would you like to override tsconfig.json with these changes?",
					Default: true,
				},
				&ok,
			)
			if err != nil {
				return errors.Wrap(err, "asking user confirmation")
			}
			if !ok {
				return nil
			}

			configFile, err = json.MarshalIndent(newTSConfig, "", "  ")
			if err != nil {
				return errors.Wrap(err, "marshalling tsconfig.json file")
			}

			if err := os.WriteFile("tsconfig.json", configFile, 0644); err != nil {
				return errors.Wrap(err, "writing tsconfig.json")
			}
			logger.Step("Updated tsconfig.json...")
		}
	} else {
		if err := os.WriteFile("tsconfig.json", configFile, 0644); err != nil {
			return errors.Wrap(err, "writing tsconfig.json")
		}
		logger.Step("Added tsconfig.json...")
	}

	return nil
}

func mergeTSConfigsRecursively(dest, src map[string]interface{}) {
	for key, value := range src {
		if subMap, isSubMap := value.(map[string]interface{}); isSubMap {
			if destSubMap, ok := dest[key]; !ok {
				dest[key] = map[string]interface{}{}
			} else if _, ok := destSubMap.(map[string]interface{}); !ok {
				dest[key] = map[string]interface{}{}
			}
			mergeTSConfigsRecursively(dest[key].(map[string]interface{}), subMap)
		} else {
			dest[key] = src[key]
		}
	}
}

// prints changes between two maps and returns whether there are differences
func printTSConfigChanges(superset, subset map[string]interface{}, parentName string) bool {
	var hasChanges bool
	b := new(bytes.Buffer)
	w := new(tabwriter.Writer)
	w.Init(b, 0, 4, 2, ' ', 0)
	for key, newVal := range superset {
		existingVal, ok := subset[key]
		keyName := key
		if parentName != "" {
			keyName = fmt.Sprintf("%s.%s", parentName, key)
		}
		if existingSubMap, isSubMap := existingVal.(map[string]interface{}); isSubMap {
			if printTSConfigChanges(newVal.(map[string]interface{}), existingSubMap, keyName) {
				hasChanges = true
			}
		} else if !ok || !reflect.DeepEqual(newVal, existingVal) {
			existingJSON, _ := json.Marshal(existingVal)
			newJSON, _ := json.Marshal(newVal)
			_, _ = w.Write([]byte(fmt.Sprintf("%s:\t(%s) -> (%s)\n", keyName, string(existingJSON), string(newJSON))))
			hasChanges = true
		}
	}
	w.Flush()
	logger.Log(b.String())
	return hasChanges
}
