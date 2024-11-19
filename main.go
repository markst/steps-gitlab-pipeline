package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/bitrise-io/go-steputils/stepconf"
	"github.com/bitrise-io/go-utils/log"
)

type config struct {
	PrivateToken string `env:"private_token,required"`
	ProjectID    string `env:"project_id,required"`   // GitLab project ID
	GitRef       string `env:"git_ref,required"`      // Branch or tag to trigger
	APIBaseURL   string `env:"api_base_url,required"` // GitLab API base URL
}

func triggerPipeline(cfg config) error {
	apiURL := fmt.Sprintf("%s/projects/%s/trigger/pipeline", cfg.APIBaseURL, url.PathEscape(cfg.ProjectID))

	data := url.Values{
		"token": {cfg.PrivateToken},
		"ref":   {cfg.GitRef},
	}

	req, err := http.NewRequest("POST", apiURL, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create request: %s", err)
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send the request: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server error: %s (code: %d)", resp.Status, resp.StatusCode)
	}

	log.Infof("Pipeline triggered successfully.")
	return nil
}

func mainE() error {
	var cfg config
	if err := stepconf.Parse(&cfg); err != nil {
		return fmt.Errorf("error parsing configuration: %s", err)
	}
	stepconf.Print(cfg)

	return triggerPipeline(cfg)
}

func main() {
	if err := mainE(); err != nil {
		fmt.Printf("Error: %+v\n", err)
		os.Exit(1)
	}
}
