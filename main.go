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
)

const (
	graphqlURL      = "https://gitlab.com/api/graphql"
	statusUpdateURL = "https://gitlab.com/api/v4/projects/%s/statuses/%s" // GitLab API to update pipeline status
)

// GraphQLResponse structure to parse the pipeline query response
type GraphQLResponse struct {
	Data struct {
		Project struct {
			Name         string `json:"name"`
			MergeRequest struct {
				Title     string `json:"title"`
				Pipelines struct {
					Nodes []struct {
						IID  string `json:"iid"`
						Jobs struct {
							Nodes []struct {
								ID         string `json:"id"`
								Name       string `json:"name"`
								Status     string `json:"status"`
								CanPlayJob bool   `json:"canPlayJob"`
							} `json:"nodes"`
						} `json:"jobs"`
					} `json:"nodes"`
				} `json:"pipelines"`
			} `json:"mergeRequest"`
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
	projectPath, mergeRequestIID, jobName, gitlabToken := fetchEnvVars()

	// Debug: Log the Bitrise build status
	bitriseBuildStatus := os.Getenv("bitrise_build_status") // Example: "0" for success, "1" for failed
	fmt.Printf("Bitrise build status: %s\n", bitriseBuildStatus)

	if bitriseBuildStatus == "" {
		log.Fatal("bitrise_build_status must be set.")
	}

	// Fetch pipelines for the merge request
	response := fetchPipelines(projectPath, mergeRequestIID, gitlabToken)

	// Debug log: Output all jobs
	debugLogJobs(response)

	// Publish Bitrise build status to GitLab
	bitriseBuildSHA := os.Getenv("bitrise_git_commit") // Commit SHA to associate with the status
	pipelineID := findPipelineID(response)
	if bitriseBuildSHA == "" || pipelineID == "" {
		log.Fatal("bitrise_git_commit and pipeline_id must be set.")
	}

	// Publish the build status first
	publishBitriseStatus(projectPath, pipelineID, bitriseBuildSHA, bitriseBuildStatusToState(bitriseBuildStatus), gitlabToken)

	// Proceed only if Bitrise build status is "0" (successful)
	if bitriseBuildStatus == "0" {
		fmt.Println("Bitrise build status indicates success. Proceeding to fetch job ID and trigger the job.")

		// Find the job to trigger
		jobID := findJobID(response, jobName)
		if jobID == "" {
			log.Fatalf("No playable job found with name '%s' in pipelines for merge request IID '%s'.", jobName, mergeRequestIID)
		}
		fmt.Printf("Found job ID: %s\n", jobID)

		// Trigger the job using the GraphQL mutation
		triggerJob(jobID, gitlabToken)
	} else {
		fmt.Printf("Bitrise build status indicates failure (%s). Skipping job trigger.\n", bitriseBuildStatus)
	}
}

// fetchEnvVars retrieves and validates the required environment variables.
func fetchEnvVars() (string, string, string, string) {
	projectPath := os.Getenv("gitlab_project_path")
	mergeRequestIID := os.Getenv("gitlab_merge_request_iid")
	jobName := os.Getenv("gitlab_job_name")
	gitlabToken := os.Getenv("gitlab_token")

	if projectPath == "" || mergeRequestIID == "" || jobName == "" || gitlabToken == "" {
		log.Fatal("gitlab_project_path, gitlab_merge_request_iid, gitlab_job_name, and gitlab_token must be set.")
	}
	return projectPath, mergeRequestIID, jobName, gitlabToken
}

// fetchPipelines sends the GraphQL query to GitLab and returns the parsed response.
func fetchPipelines(projectPath, mergeRequestIID, gitlabToken string) GraphQLResponse {
	query := `
	query GetPipelineJobs {
		project(fullPath: "` + projectPath + `") {
			name
			mergeRequest(iid: "` + mergeRequestIID + `") {
				title
				pipelines {
					nodes {
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
	}`

	requestBody := map[string]string{"query": query}
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

// debugLogJobs logs all job nodes in the pipelines for debugging.
func debugLogJobs(response GraphQLResponse) {
	fmt.Println("Debug: Listing all job nodes in pipelines:")
	for _, pipeline := range response.Data.Project.MergeRequest.Pipelines.Nodes {
		for _, job := range pipeline.Jobs.Nodes {
			fmt.Printf("  Job ID: %s, Name: %s, Status: %s, CanPlayJob: %v\n",
				job.ID, job.Name, job.Status, job.CanPlayJob)
		}
	}
}

// findJobID searches for a playable job with the specified name and returns its ID.
func findJobID(response GraphQLResponse, jobName string) string {
	for _, pipeline := range response.Data.Project.MergeRequest.Pipelines.Nodes {
		for _, job := range pipeline.Jobs.Nodes {
			if job.Name == jobName && job.CanPlayJob {
				return job.ID
			}
		}
	}
	return ""
}

// findPipelineID finds the pipeline ID from the fetched GraphQL response.
func findPipelineID(response GraphQLResponse) string {
	for _, pipeline := range response.Data.Project.MergeRequest.Pipelines.Nodes {
		return pipeline.IID // Return the first pipeline ID
	}
	return ""
}

// publishBitriseStatus sends the Bitrise build status to GitLab for the specified commit SHA and pipeline ID.
func publishBitriseStatus(projectPath, pipelineID, commitSHA, status, gitlabToken string) {
	// URL encode the projectPath
	encodedProjectPath := url.PathEscape(projectPath)
	url := fmt.Sprintf(statusUpdateURL, encodedProjectPath, commitSHA)

	// Log the URL and commitSHA
	fmt.Printf("Publishing build status to URL: %s\n", url)
	fmt.Printf("Using commit SHA: %s\n", commitSHA)

	// Build the request body
	requestBody := map[string]string{
		"state":       status,                         // Can be "success", "failed", "pending", etc.
		"target_url":  os.Getenv("bitrise_build_url"), // Optional: Link to the Bitrise build
		"description": "Bitrise build status update",
		"pipeline_id": pipelineID,
	}
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		log.Fatalf("Failed to marshal status update body: %v", err)
	}

	// Create the HTTP request
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		log.Fatalf("Failed to create status update request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+gitlabToken)

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Failed to send status update request: %v", err)
	}
	defer resp.Body.Close()

	// Check the response
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := ioutil.ReadAll(resp.Body)
		log.Fatalf("Failed to update status with status %d: %s", resp.StatusCode, string(body))
	}

	fmt.Printf("Successfully updated Bitrise build status to '%s' for commit SHA '%s' and pipeline ID '%s'.\n", status, commitSHA, pipelineID)
}

// triggerJob sends a GraphQL mutation to GitLab to play the specified job.
func triggerJob(jobID, gitlabToken string) {
	mutation := `
	mutation {
		jobPlay(input: { clientMutationId: "bitrise-trigger", id: "` + jobID + `" }) {
			job {
				id
				status
			}
			errors
		}
	}`

	requestBody := map[string]string{"query": mutation}
	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		log.Fatalf("Failed to marshal GraphQL mutation: %v", err)
	}

	req, err := http.NewRequest("POST", graphqlURL, bytes.NewBuffer(jsonBody))
	if err != nil {
		log.Fatalf("Failed to create HTTP request for mutation: %v", err)
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
		log.Fatalf("GraphQL mutation failed with status %d: %s", resp.StatusCode, string(body))
	}

	var mutationResponse GraphQLMutationResponse
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Failed to read mutation response body: %v", err)
	}
	if err := json.Unmarshal(body, &mutationResponse); err != nil {
		log.Fatalf("Failed to parse mutation response: %v", err)
	}

	if len(mutationResponse.Data.JobPlay.Errors) > 0 {
		log.Fatalf("Failed to play job. Errors: %v", mutationResponse.Data.JobPlay.Errors)
	}

	fmt.Printf("Job '%s' successfully triggered with status '%s'.\n", mutationResponse.Data.JobPlay.Job.ID, mutationResponse.Data.JobPlay.Job.Status)
}

// bitriseBuildStatusToState maps Bitrise statuses to GitLab states.
func bitriseBuildStatusToState(bitriseBuildStatus string) string {
	switch bitriseBuildStatus {
	case "0":
		return "success"
	case "1":
		return "failed"
	default:
		return "pending" // Default state for unrecognized statuses
	}
}
