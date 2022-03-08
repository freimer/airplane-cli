package latest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/airplanedev/cli/pkg/analytics"
	"github.com/airplanedev/cli/pkg/logger"
	"github.com/airplanedev/cli/pkg/version"
)

const releaseURL = "https://api.github.com/repos/airplanedev/cli/releases?per_page=1"

type release struct {
	Name       string `json:"name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// CheckLatest queries the GitHub API for newer releases and prints a warning if the CLI is outdated.
func CheckLatest(ctx context.Context) {
	if version.Get() == "<unknown>" || version.Prerelease() {
		// Pass silently if we don't know the current CLI version or are on a pre-release.
		return
	}

	latest, err := getLatest(ctx)
	if err != nil {
		analytics.ReportError(err)
		logger.Debug("An error ocurred checking for the latest version: %s", err)
		return
	} else if latest == "" {
		// Pass silently if we can't identify the latest version.
		return
	}

	// Assumes not matching latest means you are behind:
	if strings.TrimPrefix(latest, "v") != version.Get() {
		logger.Warning("A newer version of the Airplane CLI is available: %s", latest)
		logger.Suggest(
			"Visit the docs for upgrade instructions:",
			"https://docs.airplane.dev/platform/airplane-cli#upgrading-the-cli",
		)
	}
}

func getLatest(ctx context.Context) (string, error) {
	// GitHub heavily rate limits this endpoint. We should proxy this through our API and use an API key.
	// https://docs.github.com/rest/overview/resources-in-the-rest-api#rate-limiting
	req, err := http.NewRequestWithContext(ctx, "GET", releaseURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Add("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}

	if resp.StatusCode >= 400 {
		// e.g. {"message":"API rate limit ..."}
		var ghError struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&ghError); err != nil {
			analytics.ReportError(err)
			logger.Debug("Unable to decode GitHub %s API response: %s", resp.Status, err)
		}
		return "", fmt.Errorf("HTTP %s: %s", resp.Status, ghError.Message)
	}

	var releases []release
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return "", err
	}
	if len(releases) == 0 {
		return "", nil
	}
	for _, release := range releases {
		if release.Draft || release.Prerelease {
			continue
		}
		return release.Name, nil
	}
	return "", nil
}
