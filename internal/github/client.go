package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bright/reviewagent/internal/domain"
)

type Client struct {
	token   string
	http    *http.Client
	baseURL string
}

func NewClient(token string) *Client {
	return &Client{token: token, http: &http.Client{Timeout: 20 * time.Second}, baseURL: "https://api.github.com"}
}

func (c *Client) ChangedFiles(ctx context.Context, pr domain.PullRequest) ([]domain.ChangedFile, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files?per_page=100", c.baseURL, url.PathEscape(pr.Owner), url.PathEscape(pr.Repo), pr.Number)
	var files []struct {
		Filename, Status, Patch string
		Additions               int
	}
	if err := c.request(ctx, http.MethodGet, endpoint, nil, &files); err != nil {
		return nil, err
	}
	result := make([]domain.ChangedFile, 0, len(files))
	for _, file := range files {
		result = append(result, domain.ChangedFile{Path: file.Filename, Status: file.Status, Patch: file.Patch, Additions: file.Additions})
	}
	return result, nil
}

func (c *Client) CreateComment(ctx context.Context, pr domain.PullRequest, finding domain.Finding) error {
	base := fmt.Sprintf("%s/repos/%s/%s", c.baseURL, url.PathEscape(pr.Owner), url.PathEscape(pr.Repo))
	marker := findingMarker(pr, finding)
	var existing []struct {
		Body string `json:"body"`
	}
	if err := c.request(ctx, http.MethodGet, fmt.Sprintf("%s/pulls/%d/comments?per_page=100", base, pr.Number), nil, &existing); err != nil {
		return err
	}
	for _, comment := range existing {
		if strings.Contains(comment.Body, marker) {
			return nil
		}
	}
	payload := map[string]any{"body": formatFinding(finding) + "\n\n" + marker, "commit_id": pr.HeadSHA, "path": finding.File, "line": finding.NewLine, "side": "RIGHT"}
	endpoint := fmt.Sprintf("%s/pulls/%d/comments", base, pr.Number)
	return c.request(ctx, http.MethodPost, endpoint, payload, nil)
}

func findingMarker(pr domain.PullRequest, f domain.Finding) string {
	key := fmt.Sprintf("%s/%s#%d@%s:%s:%d:%s", pr.Owner, pr.Repo, pr.Number, pr.HeadSHA, f.File, f.NewLine, f.Title)
	hash := sha256.Sum256([]byte(key))
	return "<!-- reviewagent:finding:" + hex.EncodeToString(hash[:]) + " -->"
}

func (c *Client) CreateSummary(ctx context.Context, pr domain.PullRequest, body string) error {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", c.baseURL, url.PathEscape(pr.Owner), url.PathEscape(pr.Repo), pr.Number)
	marker := "<!-- reviewagent:summary:" + pr.HeadSHA + " -->"
	var existing []struct {
		Body string `json:"body"`
	}
	if err := c.request(ctx, http.MethodGet, endpoint+"?per_page=100", nil, &existing); err != nil {
		return err
	}
	for _, comment := range existing {
		if strings.Contains(comment.Body, marker) {
			return nil
		}
	}
	return c.request(ctx, http.MethodPost, endpoint, map[string]string{"body": body + "\n\n" + marker}, nil)
}

func (c *Client) request(ctx context.Context, method, endpoint string, input, output any) error {
	var payload io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("GitHub API %s: %s", resp.Status, string(body))
	}
	if output != nil && len(body) > 0 {
		return json.Unmarshal(body, output)
	}
	return nil
}

func formatFinding(f domain.Finding) string {
	severity := strings.ToUpper(string(f.Severity))
	text := "**[" + severity + "] " + f.Title + "**\n\n" + f.Body + "\n\n**Evidence:** " + strings.Join(f.Evidence, "; ")
	if f.Suggestion != "" {
		text += "\n\n**Suggestion:** " + f.Suggestion
	}
	return text
}

func Summary(files, findings int, skipped []string) string {
	result := "## ReviewAgent MVP\n\nReviewed **" + strconv.Itoa(files) + "** file(s); published **" + strconv.Itoa(findings) + "** high-confidence finding(s)."
	if len(skipped) > 0 {
		result += "\n\nSkipped: " + strings.Join(skipped, "; ")
	}
	return result
}
