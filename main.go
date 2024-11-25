package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
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
			Name          string `json:"name"`
			MergeRequests struct {
				Nodes []struct {
					IID       string `json:"iid"`
					ID        string `json:"id"`
					Title     string `json:"title"`
					Pipelines struct {
						Nodes []struct {
							ID   string `json:"id"`  // Global pipeline ID
							IID  string `json:"iid"` // Short pipeline ID
							Jobs struct {
								Nodes []struct {
									ID         string `json:"id"`         // Job global ID
									Name       string `json:"name"`       // Job name
									Status     string `json:"status"`     // Job status
									CanPlayJob bool   `json:"canPlayJob"` // Can this job be played
								} `json:"nodes"`
							} `json:"jobs"`
						} `json:"nodes"`
					} `json:"pipelines"`
				} `json:"nodes"`
			} `json:"mergeRequests"`
		} `json:"project"`
	} `json:"data"`
}

// Helper function to construct API endpoints
func constructAPIEndpoint(path string, args ...interface{}) string {
	return fmt.Sprintf(baseAPIURL+path, args...)
}

func main() {
	// Fetch environment variables
	projectPath, branchName, jobName, gitlabToken, buildStatus, buildSHA, buildURL := fetchEnvVars()

	// Determine build status state
	status := buildStatusToState(buildStatus)

	// Fetch pipelines for the merge request
	response := fetchPipelines(projectPath, branchName, gitlabToken)

	// Find the job and its associated pipeline ID
	jobID, pipelineID := findJobAndPipeline(response, jobName)
	if jobID == "" || pipelineID == "" {
		log.Fatalf("No playable job or pipeline found for job '%s' in branch '%s'.", jobName, branchName)
	}

	// Publish Bitrise build status to GitLab
	publishBuildStatus(projectPath, pipelineID, buildSHA, status, gitlabToken, buildURL)

	// Trigger the job if build status is "success"
	if status == Success {
		fmt.Println("Build status indicates success. Proceeding to trigger the job.")
		triggerJob(projectPath, jobID, gitlabToken)
	} else {
		fmt.Printf("Build status is '%s'. Skipping job trigger.\n", status)
	}
}

// fetchEnvVars retrieves and validates the required environment variables.
func fetchEnvVars() (string, string, string, string, string, string, string) {
	projectPath := os.Getenv("gitlab_project_path")
	branchName := os.Getenv("gitlab_branch_name")
	jobName := os.Getenv("gitlab_job_name")
	gitlabToken := os.Getenv("gitlab_token")
	buildStatus := os.Getenv("bitrise_build_status")
	buildSHA := os.Getenv("bitrise_git_commit")
	buildURL := os.Getenv("bitrise_build_url")

	if projectPath == "" || branchName == "" || jobName == "" || gitlabToken == "" || buildSHA == "" || buildURL == "" {
		log.Fatalf("One or more required environment variables are missing.")
	}

	return projectPath, branchName, jobName, gitlabToken, buildStatus, buildSHA, buildURL
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
func fetchPipelines(projectPath, branchName, gitlabToken string) GraphQLResponse {
	query := `
	query GetPipelineJobs($projectPath: ID!, $branchName: [String!]) {
		project(fullPath: $projectPath) {
			name
			mergeRequests(sourceBranches: $branchName, first: 1) {
				nodes {
					iid
					id
					title
					pipelines {
						nodes {
							id
							iid
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
			}
		}
	}`

	variables := map[string]interface{}{
		"projectPath": projectPath,
		"branchName":  []string{branchName},
	}
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
		body, _ := ioutil.ReadAll(resp.Body)
		log.Fatalf("GraphQL query failed with status %d: %s", resp.StatusCode, string(body))
	}

	var gqlResponse GraphQLResponse
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Failed to read response body: %v", err)
	}
	if err := json.Unmarshal(body, &gqlResponse); err != nil {
		log.Fatalf("Failed to parse GraphQL response: %v", err)
	}

	return gqlResponse
}

// findJobAndPipeline searches for a playable job with the specified name and returns its ID and associated pipeline ID.
func findJobAndPipeline(response GraphQLResponse, jobName string) (string, string) {
	for _, mergeRequest := range response.Data.Project.MergeRequests.Nodes {
		for _, pipeline := range mergeRequest.Pipelines.Nodes {
			for _, job := range pipeline.Jobs.Nodes {
				if job.Name == jobName && job.CanPlayJob {
					return job.ID, extractLastComponent(pipeline.ID)
				}
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

// publishBuildStatus sends the Bitrise build status to GitLab for the specified commit SHA and pipeline ID.
func publishBuildStatus(projectPath, pipelineID, commitSHA string, status GitLabStatus, gitlabToken, buildURL string) {
	if !status.IsValid() {
		log.Fatalf("Invalid status '%s' provided.", status)
	}

	statusUpdateEndpoint := constructAPIEndpoint(statusesPath, url.PathEscape(projectPath), commitSHA)

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
		body, _ := ioutil.ReadAll(resp.Body)
		log.Fatalf("Failed to update status. Status: %d, Response: %s", resp.StatusCode, string(body))
	}

	fmt.Printf("Successfully updated build status to '%s' for commit SHA '%s'.\n", status, commitSHA)
}

// triggerJob sends a request to play the specified job.
func triggerJob(projectPath, jobID, gitlabToken string) {
	apiURL := constructAPIEndpoint(jobsPath, url.PathEscape(projectPath), extractLastComponent(jobID))

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
		body, _ := ioutil.ReadAll(resp.Body)
		log.Fatalf("Failed to trigger job. Status: %d, Response: %s", resp.StatusCode, string(body))
	}

	fmt.Println("Job successfully triggered.")
}
