package utils

import (
	"fmt"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/lib/pkg/utils/fsx"
	"github.com/airplanedev/lib/pkg/utils/github"
	"github.com/pkg/errors"
)

func CopyFile(srcPath string, dstDirectory string) error {
	src, err := ioutil.ReadFile(srcPath)
	if err != nil {
		return errors.Wrapf(err, "reading src file %s", srcPath)
	}

	dstPath := filepath.Join(dstDirectory, filepath.Base(srcPath))
	if err := ioutil.WriteFile(dstPath, src, 0644); err != nil {
		return errors.Wrapf(err, "writing dst file %s", dstPath)
	}
	logger.Step("Copied %s", dstPath)
	return nil
}

func CopyDirectoryContents(srcDirectory string, dstDirectory string) error {
	return filepath.WalkDir(srcDirectory, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if path == srcDirectory {
			return nil
		}

		rel, err := filepath.Rel(srcDirectory, path)
		if err != nil {
			return err
		}

		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dstDirectory, rel), 0755)
		}
		return CopyFile(path, filepath.Join(dstDirectory, filepath.Dir(rel)))
	})
}

func CreateDirectory(directoryName string) error {
	if fsx.Exists(directoryName) {
		question := fmt.Sprintf("Directory %s already exists. Do you want to remove its existing files and continue?", directoryName)

		if ok, err := Confirm(question); err != nil {
			return err
		} else if !ok {
			return errors.New(fmt.Sprintf("canceled creating directory %s", directoryName))
		}
		os.RemoveAll(directoryName)
	}
	if err := os.MkdirAll(directoryName, 0755); err != nil {
		return err
	}
	logger.Step("Created directory %s", directoryName)
	return nil
}

func CopyFromGithubPath(gitPath string) error {
	if !(strings.HasPrefix(gitPath, "github.com/") || strings.HasPrefix(gitPath, "https://github.com/")) {
		return errors.New("expected path to be in the format github.com/ORG/REPO/PATH/TO/FOLDER[@REF]")
	}
	tempPath, closer, err := github.OpenGitHubDirectory(gitPath)
	if err != nil {
		return err
	}
	defer closer.Close()

	fileInfo, err := os.Stat(tempPath)
	if err != nil {
		return errors.New("path to directory or file not found")
	}

	if fileInfo.IsDir() {
		directory := filepath.Base(tempPath)
		if err := CreateDirectory(directory); err != nil {
			return err
		}
		if err := CopyDirectoryContents(tempPath, directory); err != nil {
			return err
		}

		if fsx.Exists(filepath.Join(directory, "package.json")) {
			useYarn := ShouldUseYarn(directory)
			logger.Step("Installing dependencies...")

			if err = InstallDependencies(directory, useYarn); err != nil {
				logger.Debug(err.Error())
				if useYarn {
					return errors.New("error installing dependencies using yarn. Try installing yarn.")
				}
				return err
			}
			logger.Step("Finished installing dependencies")
		}
		readmePath := filepath.Join(directory, "README.md")
		if fsx.Exists(readmePath) {
			logger.Log(logger.Gray(fmt.Sprintf("\nPreviewing %s:", readmePath)))
			readme, err := os.ReadFile(readmePath)
			if err != nil {
				return errors.Wrap(err, "reading README")
			}
			logger.Log(string(readme))
		}
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		fileName := filepath.Base(tempPath)

		if fsx.Exists(fileName) {
			question := fmt.Sprintf("File %s already exists. Do you want to overwrite it?", fileName)

			if ok, err := Confirm(question); err != nil {
				return err
			} else if !ok {
				return errors.New("canceled airplane views init")
			}
		}
		if err := CopyFile(tempPath, cwd); err != nil {
			return err
		}
	}
	return nil
}
