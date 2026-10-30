package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/bright/reviewagent/internal/domain"
)

const (
	maxReadLines     = 160
	maxReadBytes     = 24 << 10
	maxSearchFiles   = 30
	maxSearchBytes   = 120 << 10
	maxSearchResults = 12
	maxListedFiles   = 100
	maxDiffBytes     = 48 << 10
	maxSearchSnippet = 500
)

// ContextTools exposes a read-only, commit-pinned view of one repository.
// All tools are bounded; they are intentionally not a general shell or API proxy.
type ContextTools struct {
	client   *Client
	pr       domain.PullRequest
	diffs    map[string]string
	treeOnce sync.Once
	tree     []treeEntry
	treeErr  error
}

type treeEntry struct {
	Path, Type, SHA string
	Size            int
}

func NewContextTools(client *Client, pr domain.PullRequest, files []domain.ChangedFile) *ContextTools {
	diffs := make(map[string]string, len(files))
	for _, file := range files {
		if file.Patch != "" {
			diffs[file.Path] = file.Patch
		}
	}
	return &ContextTools{client: client, pr: pr, diffs: diffs}
}

func (t *ContextTools) Call(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	switch name {
	case "read_file":
		return t.callReadFile(ctx, arguments)
	case "read_diff":
		return t.callReadDiff(arguments)
	case "list_files":
		return t.callListFiles(ctx, arguments)
	case "search_code":
		return t.callSearchCode(ctx, arguments)
	default:
		return "", fmt.Errorf("tool %q is not allowed", name)
	}
}

func (t *ContextTools) callReadFile(ctx context.Context, arguments json.RawMessage) (string, error) {
	var input struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		EndLine   int    `json:"end_line"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return "", fmt.Errorf("decode read_file arguments: %w", err)
	}
	if !safeRepositoryPath(input.Path) {
		return "", fmt.Errorf("invalid repository path")
	}
	if input.StartLine < 1 {
		input.StartLine = 1
	}
	if input.EndLine < input.StartLine {
		input.EndLine = input.StartLine + maxReadLines - 1
	}
	if input.EndLine-input.StartLine+1 > maxReadLines {
		return "", fmt.Errorf("requested too many lines (maximum %d)", maxReadLines)
	}
	content, err := t.readFile(ctx, input.Path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(content, "\n")
	if input.StartLine > len(lines) {
		return "", fmt.Errorf("start_line exceeds file length %d", len(lines))
	}
	end := min(input.EndLine, len(lines))
	result := make([]string, 0, end-input.StartLine+1)
	for index := input.StartLine - 1; index < end; index++ {
		result = append(result, fmt.Sprintf("%d: %s", index+1, lines[index]))
	}
	return strings.Join(result, "\n"), nil
}

func (t *ContextTools) callReadDiff(arguments json.RawMessage) (string, error) {
	var input struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return "", fmt.Errorf("decode read_diff arguments: %w", err)
	}
	if input.Path == "" {
		paths := make([]string, 0, len(t.diffs))
		for filename := range t.diffs {
			paths = append(paths, filename)
		}
		sort.Strings(paths)
		var builder strings.Builder
		for _, filename := range paths {
			if builder.Len()+len(t.diffs[filename])+1 > maxDiffBytes {
				builder.WriteString("\n... diff truncated by tool budget ...")
				break
			}
			builder.WriteString(t.diffs[filename])
			builder.WriteByte('\n')
		}
		return builder.String(), nil
	}
	if !safeRepositoryPath(input.Path) {
		return "", fmt.Errorf("invalid repository path")
	}
	diff, ok := t.diffs[input.Path]
	if !ok {
		return "", fmt.Errorf("no patch is available for %q", input.Path)
	}
	if len(diff) > maxDiffBytes {
		return diff[:maxDiffBytes] + "\n... diff truncated by tool budget ...", nil
	}
	return diff, nil
}

func (t *ContextTools) callListFiles(ctx context.Context, arguments json.RawMessage) (string, error) {
	var input struct {
		PathPrefix string `json:"path_prefix"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return "", fmt.Errorf("decode list_files arguments: %w", err)
	}
	if input.PathPrefix != "" && !safeRepositoryPath(strings.TrimSuffix(input.PathPrefix, "/")) {
		return "", fmt.Errorf("invalid path_prefix")
	}
	tree, err := t.loadTree(ctx)
	if err != nil {
		return "", err
	}
	files := make([]string, 0, maxListedFiles)
	for _, entry := range tree {
		if entry.Type == "blob" && strings.HasPrefix(entry.Path, input.PathPrefix) {
			files = append(files, entry.Path)
			if len(files) == maxListedFiles {
				break
			}
		}
	}
	if len(files) == 0 {
		return "(no matching files)", nil
	}
	return strings.Join(files, "\n"), nil
}

func (t *ContextTools) callSearchCode(ctx context.Context, arguments json.RawMessage) (string, error) {
	var input struct {
		Query      string `json:"query"`
		PathPrefix string `json:"path_prefix"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return "", fmt.Errorf("decode search_code arguments: %w", err)
	}
	query := strings.TrimSpace(input.Query)
	if query == "" || len(query) > 200 {
		return "", fmt.Errorf("query must contain 1-200 characters")
	}
	if input.PathPrefix != "" && !safeRepositoryPath(strings.TrimSuffix(input.PathPrefix, "/")) {
		return "", fmt.Errorf("invalid path_prefix")
	}
	tree, err := t.loadTree(ctx)
	if err != nil {
		return "", err
	}
	results, searched, bytesRead := make([]string, 0, maxSearchResults), 0, 0
	for _, entry := range tree {
		if entry.Type != "blob" || !strings.HasPrefix(entry.Path, input.PathPrefix) || !searchablePath(entry.Path) || entry.Size > maxReadBytes {
			continue
		}
		if searched >= maxSearchFiles || bytesRead >= maxSearchBytes || len(results) >= maxSearchResults {
			break
		}
		content, err := t.readBlob(ctx, entry.SHA, entry.Path)
		if err != nil {
			continue
		}
		searched++
		bytesRead += len(content)
		for lineNumber, line := range strings.Split(content, "\n") {
			if strings.Contains(strings.ToLower(line), strings.ToLower(query)) {
				snippet := strings.TrimSpace(line)
				if len(snippet) > maxSearchSnippet {
					snippet = snippet[:maxSearchSnippet] + "..."
				}
				results = append(results, fmt.Sprintf("%s:%d: %s", entry.Path, lineNumber+1, snippet))
				if len(results) == maxSearchResults {
					break
				}
			}
		}
	}
	if len(results) == 0 {
		return fmt.Sprintf("(no matches; searched %d bounded source files)", searched), nil
	}
	return strings.Join(results, "\n"), nil
}

func (t *ContextTools) loadTree(ctx context.Context) ([]treeEntry, error) {
	t.treeOnce.Do(func() {
		endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/%s?recursive=1", url.PathEscape(t.pr.Owner), url.PathEscape(t.pr.Repo), url.PathEscape(t.pr.HeadSHA))
		var response struct {
			Truncated bool `json:"truncated"`
			Tree      []struct {
				Path, Type, SHA string
				Size            int
			} `json:"tree"`
		}
		if err := t.client.request(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
			t.treeErr = fmt.Errorf("load repository tree: %w", err)
			return
		}
		if response.Truncated {
			t.treeErr = fmt.Errorf("repository tree is too large for bounded search")
			return
		}
		for _, entry := range response.Tree {
			t.tree = append(t.tree, treeEntry{Path: entry.Path, Type: entry.Type, SHA: entry.SHA, Size: entry.Size})
		}
	})
	return t.tree, t.treeErr
}

func (t *ContextTools) readFile(ctx context.Context, filename string) (string, error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s?ref=%s", url.PathEscape(t.pr.Owner), url.PathEscape(t.pr.Repo), url.PathEscape(filename), url.QueryEscape(t.pr.HeadSHA))
	var response struct {
		Content, Encoding string
		Size              int
	}
	if err := t.client.request(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return "", fmt.Errorf("read file %q: %w", filename, err)
	}
	if response.Size > maxReadBytes {
		return "", fmt.Errorf("file %q exceeds read limit", filename)
	}
	return decodeGitHubContent(response.Content, response.Encoding, filename)
}

func (t *ContextTools) readBlob(ctx context.Context, sha, filename string) (string, error) {
	endpoint := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/blobs/%s", url.PathEscape(t.pr.Owner), url.PathEscape(t.pr.Repo), url.PathEscape(sha))
	var response struct {
		Content, Encoding string
		Size              int
	}
	if err := t.client.request(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return "", err
	}
	if response.Size > maxReadBytes {
		return "", fmt.Errorf("file %q exceeds read limit", filename)
	}
	return decodeGitHubContent(response.Content, response.Encoding, filename)
}

func decodeGitHubContent(content, encoding, filename string) (string, error) {
	if encoding != "base64" {
		return "", fmt.Errorf("unsupported GitHub content encoding %q", encoding)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(content, "\n", ""))
	if err != nil {
		return "", fmt.Errorf("decode file %q: %w", filename, err)
	}
	if len(decoded) > maxReadBytes {
		return "", fmt.Errorf("file %q exceeds read limit", filename)
	}
	return string(decoded), nil
}

func searchablePath(filename string) bool {
	for _, suffix := range []string{".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".java", ".rs", ".yaml", ".yml", ".json", ".sql"} {
		if strings.HasSuffix(filename, suffix) {
			return true
		}
	}
	return false
}
func safeRepositoryPath(filename string) bool {
	return filename != "" && !strings.Contains(filename, "\\") && !strings.HasPrefix(filename, "/") && path.Clean(filename) == filename && filename != "." && !strings.HasPrefix(filename, "../")
}
