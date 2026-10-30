package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bright/reviewagent/internal/domain"
)

type fakeTools struct{ calls int }

func (f *fakeTools) Call(_ context.Context, name string, arguments json.RawMessage) (string, error) {
	f.calls++
	if name != "read_file" {
		return "", &toolError{}
	}
	return "12: return repository.Save(ctx, order)", nil
}

type toolError struct{}

func (*toolError) Error() string { return "unexpected tool" }

func TestReviewerCompletesToolLoop(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"internal/order/repository.go\",\"start_line\":1,\"end_line\":20}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"findings\":[{\"title\":\"error ignored\",\"body\":\"save errors are discarded\",\"severity\":\"high\",\"confidence\":0.9,\"file\":\"service.go\",\"new_line\":4,\"evidence\":[\"diff discards err\"]}]}"}}]}`))
	}))
	defer server.Close()

	tools := &fakeTools{}
	reviewer := NewOpenAIReviewer(server.URL, "key", "test").WithTools(tools)
	findings, err := reviewer.ReviewFile(context.Background(), domain.ChangedFile{Path: "service.go", Patch: "@@ -4 +4 @@\n-old\n+_ = err\n"})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || tools.calls != 1 || len(findings) != 1 {
		t.Fatalf("requests=%d tools=%d findings=%d", requests, tools.calls, len(findings))
	}
}
