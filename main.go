
package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-utils/log"
)

type config struct {
	PrivateToken  string `env:"private_token,required"`
	RepositoryURL string `env:"repository_url,required"`
	GitRef        string `env:"git_ref,required"`
	APIURL        string `env:"api_base_url,required"`
}

func getRepo(u string) string {
	r := regexp.MustCompile(`(?::\/\/[^/]+?\/|[^:/]+?:)([^/]+?\/.+?)(?:\.git)?\/?$`)
	if matches := r.FindStringSubmatch(u); len(matches) == 2 {
		return matches[1]
	}
	return ""
}

func triggerPipeline(cfg config) error {
	repo := url.PathEscape(getRepo(cfg.RepositoryURL))
	apiURL := fmt.Sprintf("%s/projects/%s/trigger/pipeline", cfg.APIURL, repo)

	data := map[string]string{
		"token": cfg.PrivateToken,
		"ref":   cfg.GitRef,
	}

	if os.Getenv("BITRISE_API_TOKEN") != "" {
		data["variables[BITRISE_API_TOKEN]"] = os.Getenv("BITRISE_API_TOKEN")
	}
	if os.Getenv("BITRISE_APP_SLUG") != "" {
		data["variables[BITRISE_APP_SLUG]"] = os.Getenv("BITRISE_APP_SLUG")
	}
	if os.Getenv("BITRISE_BUILD_SLUG") != "" {
		data["variables[BITRISE_BUILD_SLUG]"] = os.Getenv("BITRISE_BUILD_SLUG")
	}

	form := url.Values{}
	for key, value := range data {
		form.Set(key, value)
	}

	req, err := http.NewRequest("POST", apiURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create request: %s", err)
	}
	req.Header.Add("PRIVATE-TOKEN", cfg.PrivateToken)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send the request: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := ioutil.ReadAll(resp.Body)
		return fmt.Errorf("server error: %s url: %s code: %d body: %s", resp.Status, apiURL, resp.StatusCode, string(body))
	}

	log.Infof("Pipeline triggered successfully.")
	return nil
}

func main() {
	var cfg config
	if err := stepconf.Parse(&cfg); err != nil {
		log.Errorf("Error: %s\n", err)
		os.Exit(1)
	}
	stepconf.Print(cfg)

	if err := triggerPipeline(cfg); err != nil {
		log.Errorf("Failed to trigger pipeline, error: %s", err)
		os.Exit(1)
	}
}
