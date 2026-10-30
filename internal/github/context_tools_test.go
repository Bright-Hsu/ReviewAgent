package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bright/reviewagent/internal/domain"
)

func TestSafeRepositoryPath(t *testing.T) {
	valid := []string{"internal/order/service.go", ".github/workflows/review.yml"}
	for _, filename := range valid {
		if !safeRepositoryPath(filename) {
			t.Fatalf("expected valid path %q", filename)
		}
	}
	invalid := []string{"../secret", "internal/../secret", "/etc/passwd", `internal\\secret`, "."}
	for _, filename := range invalid {
		if safeRepositoryPath(filename) {
			t.Fatalf("expected invalid path %q", filename)
		}
	}
}

func TestContextToolsReadDiffListAndSearchAreCommitPinned(t *testing.T) {
	treeRequests, blobRequests := 0, 0
	client := &Client{token: "token", http: &http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		var payload any
		switch {
		case strings.Contains(request.URL.Path, "/git/trees/head-sha"):
			treeRequests++
			payload = map[string]any{"truncated": false, "tree": []map[string]any{{"path": "internal/service.go", "type": "blob", "sha": "blob-1", "size": 40}, {"path": "README.md", "type": "blob", "sha": "blob-2", "size": 20}}}
		case strings.Contains(request.URL.Path, "/git/blobs/blob-1"):
			blobRequests++
			payload = map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte("func SaveOrder() error { return nil }\n")), "size": 39}
		default:
			t.Fatalf("unexpected request: %s", request.URL.String())
		}
		body, _ := json.Marshal(payload)
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: request}, nil
	})}}
	tools := NewContextTools(client, domain.PullRequest{Owner: "acme", Repo: "orders", HeadSHA: "head-sha"}, []domain.ChangedFile{{Path: "internal/service.go", Patch: "@@ -1 +1 @@\n-old\n+new\n"}})

	diff, err := tools.Call(context.Background(), "read_diff", []byte(`{"path":"internal/service.go"}`))
	if err != nil || !strings.Contains(diff, "+new") {
		t.Fatalf("diff=%q err=%v", diff, err)
	}
	files, err := tools.Call(context.Background(), "list_files", []byte(`{"path_prefix":"internal/"}`))
	if err != nil || files != "internal/service.go" {
		t.Fatalf("files=%q err=%v", files, err)
	}
	matches, err := tools.Call(context.Background(), "search_code", []byte(`{"query":"SaveOrder","path_prefix":"internal/"}`))
	if err != nil || !strings.Contains(matches, "internal/service.go:1") {
		t.Fatalf("matches=%q err=%v", matches, err)
	}
	if treeRequests != 1 || blobRequests != 1 {
		t.Fatalf("tree=%d blob=%d", treeRequests, blobRequests)
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (r roundTripper) RoundTrip(request *http.Request) (*http.Response, error) { return r(request) }
