package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bright/reviewagent/internal/domain"
	"github.com/bright/reviewagent/internal/review"
)

func TestValidSignature(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	if !validSignature(body, "sha256=2677ad3e7c090b2fa2c0fb13020d66d5420879b8316eb356a2d60fb9073bc778", "secret") {
		t.Fatal("expected a valid signature")
	}
	if validSignature(body, "sha256=deadbeef", "secret") {
		t.Fatal("expected invalid signature")
	}
}

func TestGetRunRequiresAdminTokenAndReturnsPersistedRun(t *testing.T) {
	service, err := NewService(Config{DatabaseURL: "file:app-run-test?mode=memory&cache=shared", AdminToken: "admin", Policy: review.Policy{MinConfidence: .75, MaxFiles: 20, MaxPatchLines: 1200}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	run, claimed, err := service.store.Claim(context.Background(), domain.PullRequest{Owner: "acme", Repo: "orders", Number: 12, HeadSHA: "abc"})
	if err != nil || !claimed {
		t.Fatalf("claim=%v err=%v", claimed, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/runs/"+run.ID, nil)
	unauthorized := httptest.NewRecorder()
	service.Routes().ServeHTTP(unauthorized, req)
	if unauthorized.Code != http.StatusNotFound {
		t.Fatalf("got %d", unauthorized.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/runs/"+run.ID, nil)
	req.Header.Set("Authorization", "Bearer admin")
	response := httptest.NewRecorder()
	service.Routes().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("got %d: %s", response.Code, response.Body.String())
	}
	if response.Body.String() == "" {
		t.Fatal("expected JSON run")
	}
}

func TestEnqueueDeduplicatesAndCancelsOlderHead(t *testing.T) {
	service, err := NewService(Config{DatabaseURL: "file:app-queue-test?mode=memory&cache=shared", Policy: review.Policy{MinConfidence: .75, MaxFiles: 20, MaxPatchLines: 1200}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	pr := domain.PullRequest{Owner: "acme", Repo: "orders", Number: 12, HeadSHA: "first"}
	first, created, err := service.Enqueue(context.Background(), pr)
	if err != nil || !created {
		t.Fatalf("enqueue=%v err=%v", created, err)
	}
	_, created, err = service.Enqueue(context.Background(), pr)
	if err != nil || created {
		t.Fatalf("duplicate enqueue=%v err=%v", created, err)
	}
	pr.HeadSHA = "second"
	if _, created, err = service.Enqueue(context.Background(), pr); err != nil || !created {
		t.Fatalf("new head enqueue=%v err=%v", created, err)
	}
	old, err := service.store.Get(context.Background(), first.ID)
	if err != nil || old.Status != domain.RunCancelled {
		t.Fatalf("old run=%+v err=%v", old, err)
	}
}
