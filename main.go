package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/bitrise-io/go-steputils/stepconf"
)

type config struct {
	PrivateToken string `env:"private_token,required"`
	ProjectID    string `env:"project_id,required"`   // GitLab project ID
	GitRef       string `env:"git_ref,required"`      // Branch or tag to trigger
	APIBaseURL   string `env:"api_base_url,required"` // GitLab API base URL
}

func triggerPipeline(cfg config) error {
	apiURL := fmt.Sprintf("%s/projects/%s/trigger/pipeline", cfg.APIBaseURL, cfg.ProjectID)

	// Determine the ref to use
	ref := cfg.GitRef
	if ref == "" {
		ref = os.Getenv("BITRISE_GIT_BRANCH")
		fmt.Printf("DEBUG: Using BITRISE_GIT_BRANCH as ref: %s\n", ref)
	}
	if ref == "" {
		return fmt.Errorf("ref is required but not provided and BITRISE_GIT_BRANCH is also empty")
	}

	// Prepare form data with variables
	data := url.Values{
		"token":                         {cfg.PrivateToken},
		"ref":                           {ref},
		"variables[BITRISE_API_TOKEN]":  {os.Getenv("BITRISE_API_TOKEN")},
		"variables[BITRISE_APP_SLUG]":   {os.Getenv("BITRISE_APP_SLUG")},
		"variables[BITRISE_BUILD_SLUG]": {os.Getenv("BITRISE_BUILD_SLUG")},
	}

	// Debug log for the form data
	fmt.Printf("DEBUG: Sending pipeline trigger with data: %s\n", data.Encode())

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

	// Debug log for response status
	fmt.Printf("DEBUG: Response Status: %s\n", resp.Status)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("server error: %s (code: %d)", resp.Status, resp.StatusCode)
	}

	fmt.Println("Pipeline triggered successfully.")
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
