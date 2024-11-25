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
	graphqlURL      = "https://gitlab.com/api/graphql"
	statusUpdateURL = "https://gitlab.com/api/v4/projects/%s/statuses/%s" // GitLab API to update pipeline status
)

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

// GraphQLMutationResponse structure to parse the mutation response
type GraphQLMutationResponse struct {
	Data struct {
		JobPlay struct {
			Job struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"job"`
			Errors []string `json:"errors"`
		} `json:"jobPlay"`
	} `json:"data"`
}

func main() {
	// Fetch environment variables
	projectPath, branchName, jobName, gitlabToken, buildStatus, buildSHA, buildURL := fetchEnvVars()

	// Debug: Log all environment variables
	fmt.Println("All Environment Variables:")
	for _, env := range os.Environ() {
		fmt.Println(env)
	}

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
	if status == "success" {
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
func buildStatusToState(buildStatus string) string {
	// The state of the status. Can be one of the following: pending, running, success, failed, canceled, skipped
	switch buildStatus {
	case "0":
		return "success"
	case "1":
		return "failed"
	default:
		return "pending"
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

	// Prepare variables
	variables := map[string]interface{}{
		"projectPath": projectPath,
		"branchName":  []string{branchName}, // Pass branch name as an array
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
	responseBody := string(body)
	fmt.Printf("Fetch pipelines", responseBody)

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
					// Extract the numeric pipeline ID from the full ID
					pipelineID := extractLastComponent(pipeline.ID)
					return job.ID, pipelineID
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
func publishBuildStatus(projectPath, pipelineID, commitSHA, status, gitlabToken, buildURL string) {
	encodedProjectPath := url.PathEscape(projectPath)
	statusUpdateEndpoint := fmt.Sprintf(statusUpdateURL, encodedProjectPath, commitSHA)

	// Debug logs
	fmt.Printf("Publishing build status to URL: %s\n", statusUpdateEndpoint)
	fmt.Printf("Commit SHA: %s\n", commitSHA)

	// Build request body
	formData := url.Values{}
	formData.Set("name", "Bitrise.io")
	formData.Set("state", status)
	formData.Set("target_url", buildURL)
	formData.Set("description", "Bitrise build status update")
	formData.Set("pipeline_id", pipelineID)

	// Debug: Log request body
	fmt.Printf("Request Body: %s\n", formData.Encode())

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

	body, _ := ioutil.ReadAll(resp.Body)
	fmt.Printf("Response status: %d\n", resp.StatusCode)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		log.Fatalf("Failed to update status with status %d: %s", resp.StatusCode, string(body))
	}

	fmt.Printf("Successfully updated build status to '%s' for commit SHA '%s'.\n", status, commitSHA)
}

// triggerJob sends a GraphQL mutation to GitLab to play the specified job.
func triggerJob(projectID, jobID, gitlabToken string) {
	// Build the API URL for triggering the job
	apiURL := fmt.Sprintf("https://gitlab.com/api/v4/projects/%s/jobs/%s/play", url.PathEscape(projectID), extractLastComponent(jobID))
	fmt.Printf("Triggering job with id '%s' - url '%s'.\n", jobID, apiURL)

	// Prepare the job variables
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

	// Prepare the request body
	requestBody := map[string]interface{}{
		"job_variables_attributes": jobVariables,
	}
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		log.Fatalf("Failed to marshal request body: %v", err)
	}

	// Debug: Output the request payload
	fmt.Printf("DEBUG: Request payload: %s\n", string(jsonBody))

	// Create the HTTP request
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		log.Fatalf("Failed to create HTTP request for job trigger: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("PRIVATE-TOKEN", gitlabToken)

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Failed to send the job trigger request: %v", err)
	}
	defer resp.Body.Close()

	// Check the response status code
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := ioutil.ReadAll(resp.Body)
		log.Fatalf("Failed to trigger job. Status: %d, Response: %s", resp.StatusCode, string(body))
	}

	// Parse the response
	body, err := ioutil.ReadAll(resp.Body)
	responseBody := string(body)
	fmt.Printf("Triggered job response", responseBody)

	if err != nil {
		log.Fatalf("Failed to read response body: %v", err)
	}

	// Debug output for the response
	fmt.Printf("Job trigger response: %s\n", string(body))

	fmt.Println("Job successfully triggered.")
}
