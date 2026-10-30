package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bright/reviewagent/internal/domain"
)

func TestCreateCommentIsIdempotentAcrossRetries(t *testing.T) {
	var bodies []string
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(func() []map[string]string {
				result := make([]map[string]string, 0, len(bodies))
				for _, body := range bodies {
					result = append(result, map[string]string{"body": body})
				}
				return result
			}())
			return
		}
		posts++
		var payload struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		bodies = append(bodies, payload.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	client := NewClient("token")
	client.baseURL, client.http = server.URL, server.Client()
	pr := domain.PullRequest{Owner: "acme", Repo: "orders", Number: 3, HeadSHA: "abc"}
	finding := domain.Finding{Title: "handle error", Body: "error is ignored", File: "x.go", NewLine: 9}
	for i := 0; i < 2; i++ {
		if err := client.CreateComment(context.Background(), pr, finding); err != nil {
			t.Fatal(err)
		}
	}
	if posts != 1 || len(bodies) != 1 || !strings.Contains(bodies[0], "reviewagent:finding:") {
		t.Fatalf("posts=%d bodies=%v", posts, bodies)
	}
}
