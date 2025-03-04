package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const (
	baseAPIURL   = "https://gitlab.com/api/v4/projects"
	graphqlURL   = "https://gitlab.com/api/graphql"
	statusesPath = "/%s/statuses/%s"  // Format: /:projectPath/statuses/:commitSHA
	jobsPath     = "/%s/jobs/%s/play" // Format: /:projectPath/jobs/:jobID/play
)

// GitLabStatus represents the possible states in GitLab
type GitLabStatus string

const (
	Pending  GitLabStatus = "pending"
	Running  GitLabStatus = "running"
	Success  GitLabStatus = "success"
	Failed   GitLabStatus = "failed"
	Canceled GitLabStatus = "canceled"
	Skipped  GitLabStatus = "skipped"
)

// IsValid checks if the status is valid
func (s GitLabStatus) IsValid() bool {
	switch s {
	case Pending, Running, Success, Failed, Canceled, Skipped:
		return true
	}
	return false
}

// GraphQLResponse structure to parse the pipeline query response
type GraphQLResponse struct {
	Data struct {
		Project struct {
			Name      string `json:"name"`
			Pipelines struct {
				Nodes []struct {
					ID     string `json:"id"`  // Global pipeline ID
					IID    string `json:"iid"` // Short pipeline ID
					Status string `json:"status"`
					Jobs   struct {
						Nodes []struct {
							ID         string `json:"id"`         // Job global ID
							Name       string `json:"name"`       // Job name
							Status     string `json:"status"`     // Job status
							CanPlayJob bool   `json:"canPlayJob"` // Can this job be played
						} `json:"nodes"`
					} `json:"jobs"`
				} `json:"nodes"`
			} `json:"pipelines"`
		} `json:"project"`
	} `json:"data"`
}

func main() {
	// Fetch environment variables
	projectPath, branchName, jobName, gitlabToken, buildStatus, buildSHA, buildURL := fetchEnvVars()

	// Determine build status state
	status := buildStatusToState(buildStatus)

	// Fetch pipelines for the commit
	response := fetchPipelines(projectPath, *buildSHA, branchName, gitlabToken)

	// Find the job and its associated pipeline ID
	jobID, pipelineID := findJobAndPipeline(response, jobName)

	log.Printf("Build Job id '%s'", jobID)
	log.Printf("Build SHA '%s'", safeString(buildSHA, "not provided"))
	log.Printf("Build Branch '%s'", safeString(branchName, "not provided"))
	log.Printf("Build URL '%s'", buildURL)
	log.Printf("Build Status '%s'", status)
	log.Printf("Build Pipelines '%s'", pipelineID)

	if jobID == "" || pipelineID == "" {
		log.Fatalf("No playable job or pipeline found for job '%s'", jobName)
	}

	// Publish Bitrise build status to GitLab
	// publishBuildStatus(projectPath, pipelineID, buildSHA, status, gitlabToken, buildURL)

	// Trigger the job if build status is "success"
	if status == Success {
		fmt.Println("Build status indicates success. Proceeding to trigger the job.")
		triggerJob(projectPath, jobID, gitlabToken)
	} else {
		fmt.Printf("Build status is '%s'. Skipping job trigger.\n", status)
	}
}

// fetchEnvVars retrieves and validates the required environment variables.
func fetchEnvVars() (string, *string, string, string, string, *string, string) {
	// Fetch environment variables
	projectPath := os.Getenv("gitlab_project_path")
	branchName := os.Getenv("gitlab_branch_name")
	jobName := os.Getenv("gitlab_job_name")
	gitlabToken := os.Getenv("gitlab_token")
	buildStatus := os.Getenv("bitrise_build_status")
	buildSHA := os.Getenv("bitrise_git_commit")
	buildURL := os.Getenv("bitrise_build_url")
	pr := os.Getenv("PR")

	// Track missing variables
	missingVars := []string{}
	if projectPath == "" {
		missingVars = append(missingVars, "gitlab_project_path")
	}
	if jobName == "" {
		missingVars = append(missingVars, "gitlab_job_name")
	}
	if gitlabToken == "" {
		missingVars = append(missingVars, "gitlab_token")
	}
	if buildURL == "" {
		missingVars = append(missingVars, "bitrise_build_url")
	}

	// Report missing variables if any
	if len(missingVars) > 0 {
		log.Fatalf("The following required environment variables are missing: %v", missingVars)
	}

	// Make buildSHA optional
	var buildSHAPtr *string
	if buildSHA != "" {
		buildSHAPtr = &buildSHA
	} else {
		buildSHAPtr = nil
	}

	// Determine branchName based on PR environment variable
	var branchNamePtr *string
	if pr == "false" {
		branchNamePtr = nil
	} else {
		branchNamePtr = &branchName
	}

	return projectPath, branchNamePtr, jobName, gitlabToken, buildStatus, buildSHAPtr, buildURL
}

// buildStatusToState maps the Bitrise build status (as a string) to GitLab states.
func buildStatusToState(buildStatus string) GitLabStatus {
	switch buildStatus {
	case "0":
		return Success
	case "1":
		return Failed
	default:
		return Pending
	}
}

// fetchPipelines sends the GraphQL query to GitLab and returns the parsed response.
func fetchPipelines(projectPath string, sha string, branchName *string, gitlabToken string) GraphQLResponse {
	query := `
	query GetPipelinesForCommit($projectPath: ID!, $sha: String!) {
		project(fullPath: $projectPath) {
			name
			pipelines(sha: $sha) {
				nodes {
					id
					iid
					status
					jobs {
						nodes {
							id
							name
							status
							canPlayJob
						}
					}
				}
			}
		}
	}`

	// Construct the variables map
	var variables = map[string]interface{}{
		"projectPath": projectPath,
		"sha":         sha,
	}
	if branchName != nil && *branchName != "" {
		variables["branchName"] = []string{*branchName}
	}

	// Build the request body
	requestBody := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		log.Fatalf("Failed to marshal GraphQL query: %v", err)
	}

	req, err := http.NewRequest("POST", graphqlURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		log.Fatalf("Failed to create HTTP request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gitlabToken)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("GraphQL query failed with status %d: %s", resp.StatusCode, string(body))
	}

	var gqlResponse GraphQLResponse
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Failed to read response body: %v", err)
	}
	if err := json.Unmarshal(body, &gqlResponse); err != nil {
		log.Fatalf("Failed to parse GraphQL response: %v", err)
	}

	return gqlResponse
}

// findJobAndPipeline searches for a playable job and returns its ID and associated pipeline ID.
func findJobAndPipeline(response GraphQLResponse, jobName string) (string, string) {
	for _, pipeline := range response.Data.Project.Pipelines.Nodes {
		for _, job := range pipeline.Jobs.Nodes {
			if job.Name == jobName { // } && job.CanPlayJob {
				return job.ID, extractLastComponent(pipeline.ID)
			}
		}
	}
	return "", ""
}

// extractLastComponent extracts the last component of a string separated by '/'
func extractLastComponent(fullID string) string {
	parts := strings.Split(fullID, "/")
	return parts[len(parts)-1]
}

func safeString(ptr *string, fallback string) string {
	if ptr == nil {
		return fallback
	}
	return *ptr
}

// publishBuildStatus sends the Bitrise build status to GitLab for the specified commit SHA and pipeline ID.
func publishBuildStatus(projectPath, pipelineID, commitSHA string, status GitLabStatus, gitlabToken, buildURL string) {
	if !status.IsValid() {
		log.Fatalf("Invalid status '%s' provided.", status)
	}

	statusUpdateEndpoint := fmt.Sprintf(baseAPIURL+statusesPath, url.PathEscape(projectPath), commitSHA)

	formData := url.Values{}
	formData.Set("name", "Bitrise.io")
	formData.Set("state", string(status)) // Convert GitLabStatus to string
	formData.Set("target_url", buildURL)
	formData.Set("description", "Bitrise build status update")
	formData.Set("pipeline_id", pipelineID)

	req, err := http.NewRequest("POST", statusUpdateEndpoint, bytes.NewBufferString(formData.Encode()))
	if err != nil {
		log.Fatalf("Failed to create status update request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+gitlabToken)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Failed to send status update request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("Failed to update status. Status: %d, Response: %s", resp.StatusCode, string(body))
	}

	fmt.Printf("Successfully updated build status to '%s' for commit SHA '%s'.\n", status, commitSHA)
}

// triggerJob sends a request to play the specified job.
func triggerJob(projectPath, jobID, gitlabToken string) {
	apiURL := fmt.Sprintf(baseAPIURL+jobsPath, url.PathEscape(projectPath), extractLastComponent(jobID))
	fmt.Printf("Triggering job with id '%s' - url '%s'.\n", jobID, apiURL)

	jobVariables := []map[string]string{
		{"key": "BITRISE_API_TOKEN", "value": os.Getenv("BITRISE_API_TOKEN")},
		{"key": "BITRISE_APP_SLUG", "value": os.Getenv("BITRISE_APP_SLUG")},
		{"key": "BITRISE_BUILD_SLUG", "value": os.Getenv("BITRISE_BUILD_SLUG")},
	}

	// Ensure all required variables are set
	for _, v := range jobVariables {
		if v["value"] == "" {
			log.Fatalf("Environment variable %s must be set.", v["key"])
		}
	}

	fmt.Println("Job Variables:")
	for _, v := range jobVariables {
		fmt.Printf("%s: %s\n", v["key"], v["value"])
	}

	requestBody := map[string]interface{}{
		"job_variables_attributes": jobVariables,
	}
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		log.Fatalf("Failed to marshal request body: %v", err)
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		log.Fatalf("Failed to create HTTP request for job trigger: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("PRIVATE-TOKEN", gitlabToken)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Failed to send job trigger request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("Failed to trigger job. Status: %d, Response: %s", resp.StatusCode, string(body))
	}

	fmt.Println("Job successfully triggered.")
}
