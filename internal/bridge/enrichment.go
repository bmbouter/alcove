// Copyright 2026 Brian Bouterse
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const (
	maxBodyLen     = 5000
	maxCommentLen  = 2000
	maxCommentsNum = 20
	maxCILogLen    = 3000
)

// enrichEventContext fetches rich context from GitHub for the given event
// and returns a structured preamble string that provides the LLM with
// full issue/PR details without needing to make its own API calls.
func (p *GitHubPoller) enrichEventContext(ctx context.Context, token, baseURL, eventType, action string, meta map[string]string) string {
	issueNum := meta["GITHUB_ISSUE_NUMBER"]
	prNum := meta["GITHUB_PR_NUMBER"]
	repo := meta["GITHUB_REPO"]

	var sb strings.Builder
	sb.WriteString("## Event Context\n\n")
	sb.WriteString(fmt.Sprintf("**Event**: %s / %s\n", eventType, action))
	sb.WriteString(fmt.Sprintf("**Repository**: %s\n", repo))

	if label := meta["GITHUB_LABEL_ADDED"]; label != "" {
		sb.WriteString(fmt.Sprintf("**Label Added**: %s\n", label))
	}
	sb.WriteString("\n")

	switch {
	case issueNum != "" && (eventType == "issues" || eventType == "issue_comment"):
		p.enrichIssueContext(ctx, token, baseURL, repo, issueNum, &sb)
	case prNum != "" && (eventType == "pull_request" || eventType == "pull_request_review" || eventType == "pull_request_review_comment"):
		p.enrichPRContext(ctx, token, baseURL, repo, prNum, meta["GITHUB_SHA"], &sb)
	default:
		sb.WriteString("No additional context available for this event type.\n")
	}

	sb.WriteString("\n---\n")
	return sb.String()
}

func (p *GitHubPoller) enrichIssueContext(ctx context.Context, token, baseURL, repo, issueNum string, sb *strings.Builder) {
	// Fetch issue details
	data, err := p.githubAPIGet(ctx, token, fmt.Sprintf("%s/repos/%s/issues/%s", baseURL, repo, issueNum))
	if err != nil {
		log.Printf("poller: enrichment: could not fetch issue #%s: %v", issueNum, err)
		sb.WriteString(fmt.Sprintf("Could not fetch issue #%s: %v\n", issueNum, err))
		return
	}

	var issue struct {
		Title     string                             `json:"title"`
		Body      string                             `json:"body"`
		State     string                             `json:"state"`
		Labels    []struct{ Name string `json:"name"` } `json:"labels"`
		Assignees []struct{ Login string `json:"login"` } `json:"assignees"`
		User      struct{ Login string `json:"login"` }  `json:"user"`
	}
	if err := json.Unmarshal(data, &issue); err != nil {
		log.Printf("poller: enrichment: error parsing issue #%s: %v", issueNum, err)
		return
	}

	sb.WriteString(fmt.Sprintf("### Issue #%s: %s\n\n", issueNum, issue.Title))
	sb.WriteString(fmt.Sprintf("**State**: %s\n", issue.State))
	sb.WriteString(fmt.Sprintf("**Author**: @%s\n", issue.User.Login))

	if len(issue.Labels) > 0 {
		var labelNames []string
		for _, l := range issue.Labels {
			labelNames = append(labelNames, l.Name)
		}
		sb.WriteString(fmt.Sprintf("**Labels**: %s\n", strings.Join(labelNames, ", ")))
	}
	if len(issue.Assignees) > 0 {
		var assigneeNames []string
		for _, a := range issue.Assignees {
			assigneeNames = append(assigneeNames, "@"+a.Login)
		}
		sb.WriteString(fmt.Sprintf("**Assignees**: %s\n", strings.Join(assigneeNames, ", ")))
	}

	sb.WriteString("\n**Body**:\n")
	body := issue.Body
	if len(body) > maxBodyLen {
		body = body[:maxBodyLen] + "\n... (truncated)"
	}
	if body == "" {
		body = "(empty)"
	}
	sb.WriteString(body + "\n")

	// Fetch comments
	commentsData, err := p.githubAPIGet(ctx, token, fmt.Sprintf("%s/repos/%s/issues/%s/comments?per_page=%d", baseURL, repo, issueNum, maxCommentsNum))
	if err != nil {
		log.Printf("poller: enrichment: could not fetch comments for issue #%s: %v", issueNum, err)
		return
	}

	var comments []struct {
		Body string                            `json:"body"`
		User struct{ Login string `json:"login"` } `json:"user"`
		CreatedAt string                       `json:"created_at"`
	}
	if err := json.Unmarshal(commentsData, &comments); err != nil {
		log.Printf("poller: enrichment: error parsing comments for issue #%s: %v", issueNum, err)
		return
	}

	if len(comments) > 0 {
		sb.WriteString(fmt.Sprintf("\n### Comments (%d)\n\n", len(comments)))
		for _, c := range comments {
			comment := c.Body
			if len(comment) > maxCommentLen {
				comment = comment[:maxCommentLen] + "\n... (truncated)"
			}
			dateStr := c.CreatedAt
			if len(dateStr) >= 10 {
				dateStr = dateStr[:10]
			}
			sb.WriteString(fmt.Sprintf("**@%s** (%s):\n%s\n\n", c.User.Login, dateStr, comment))
		}
	}
}

func (p *GitHubPoller) enrichPRContext(ctx context.Context, token, baseURL, repo, prNum, sha string, sb *strings.Builder) {
	// Fetch PR details
	data, err := p.githubAPIGet(ctx, token, fmt.Sprintf("%s/repos/%s/pulls/%s", baseURL, repo, prNum))
	if err != nil {
		log.Printf("poller: enrichment: could not fetch PR #%s: %v", prNum, err)
		sb.WriteString(fmt.Sprintf("Could not fetch PR #%s: %v\n", prNum, err))
		return
	}

	var pr struct {
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Labels []struct{ Name string `json:"name"` } `json:"labels"`
		User   struct{ Login string `json:"login"` }  `json:"user"`
		Merged bool                                   `json:"merged"`
	}
	if err := json.Unmarshal(data, &pr); err != nil {
		log.Printf("poller: enrichment: error parsing PR #%s: %v", prNum, err)
		return
	}

	sb.WriteString(fmt.Sprintf("### PR #%s: %s\n\n", prNum, pr.Title))
	sb.WriteString(fmt.Sprintf("**State**: %s (merged: %v)\n", pr.State, pr.Merged))
	sb.WriteString(fmt.Sprintf("**Branch**: %s → %s\n", pr.Head.Ref, pr.Base.Ref))
	sb.WriteString(fmt.Sprintf("**Author**: @%s\n", pr.User.Login))
	sb.WriteString(fmt.Sprintf("**Head SHA**: %s\n", pr.Head.SHA))

	if len(pr.Labels) > 0 {
		var labelNames []string
		for _, l := range pr.Labels {
			labelNames = append(labelNames, l.Name)
		}
		sb.WriteString(fmt.Sprintf("**Labels**: %s\n", strings.Join(labelNames, ", ")))
	}

	body := pr.Body
	if len(body) > maxBodyLen {
		body = body[:maxBodyLen] + "\n... (truncated)"
	}
	if body != "" {
		sb.WriteString(fmt.Sprintf("\n**Body**:\n%s\n", body))
	}

	// Fetch reviews
	reviewsData, _ := p.githubAPIGet(ctx, token, fmt.Sprintf("%s/repos/%s/pulls/%s/reviews", baseURL, repo, prNum))
	if reviewsData != nil {
		var reviews []struct {
			State string                             `json:"state"`
			Body  string                             `json:"body"`
			User  struct{ Login string `json:"login"` } `json:"user"`
		}
		if err := json.Unmarshal(reviewsData, &reviews); err == nil && len(reviews) > 0 {
			sb.WriteString("\n### Reviews\n\n")
			for _, r := range reviews {
				if r.Body != "" {
					reviewBody := r.Body
					if len(reviewBody) > maxCommentLen {
						reviewBody = reviewBody[:maxCommentLen] + "\n... (truncated)"
					}
					sb.WriteString(fmt.Sprintf("**@%s** (%s):\n%s\n\n", r.User.Login, r.State, reviewBody))
				}
			}
		}
	}

	// Fetch CI status
	checkSHA := pr.Head.SHA
	if checkSHA == "" {
		checkSHA = sha
	}
	if checkSHA != "" {
		checksData, _ := p.githubAPIGet(ctx, token, fmt.Sprintf("%s/repos/%s/commits/%s/check-runs", baseURL, repo, checkSHA))
		if checksData != nil {
			var checks struct {
				CheckRuns []struct {
					Name       string `json:"name"`
					Conclusion string `json:"conclusion"`
					Status     string `json:"status"`
				} `json:"check_runs"`
			}
			if err := json.Unmarshal(checksData, &checks); err == nil && len(checks.CheckRuns) > 0 {
				sb.WriteString("\n### CI Status\n\n")
				for _, cr := range checks.CheckRuns {
					status := cr.Conclusion
					if status == "" {
						status = cr.Status
					}
					sb.WriteString(fmt.Sprintf("- %s (%s)\n", cr.Name, status))
				}
			}
		}
	}
}

// githubAPIGet performs an authenticated GET to the GitHub API.
func (p *GitHubPoller) githubAPIGet(ctx context.Context, token, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "alcove-poller")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}
	return respBody, nil
}
