package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bright/reviewagent/internal/domain"
	"github.com/bright/reviewagent/internal/github"
	"github.com/bright/reviewagent/internal/llm"
	"github.com/bright/reviewagent/internal/review"
	"github.com/bright/reviewagent/internal/store"
)

type Service struct {
	config   Config
	logger   *slog.Logger
	github   *github.Client
	reviewer review.Reviewer
	store    store.Store
	activeMu sync.Mutex
	active   map[string]context.CancelFunc
}

func NewService(config Config, logger *slog.Logger) (*Service, error) {
	runStore, err := store.OpenSQLite(config.DatabaseURL)
	if err != nil {
		return nil, err
	}
	s := &Service{config: config, logger: logger, github: github.NewClient(config.GitHubToken), store: runStore, active: make(map[string]context.CancelFunc)}
	if config.LLMAPIKey != "" {
		s.reviewer = llm.NewOpenAIReviewer(config.LLMBaseURL, config.LLMAPIKey, config.LLMModel)
	}
	return s, nil
}

const workerLease = 2 * time.Minute

// Start runs the persistent single-process worker until ctx is cancelled.
func (s *Service) Start(ctx context.Context) {
	if err := s.store.RecoverExpired(ctx, time.Now().UTC()); err != nil {
		s.logger.Error("recover expired review runs", "error", err)
	}
	for {
		if err := s.store.RecoverExpired(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			s.logger.Error("recover expired review runs", "error", err)
		}
		run, ok, err := s.store.ClaimNext(ctx, workerLease)
		if err != nil {
			s.logger.Error("claim review run", "error", err)
		}
		if err == nil && ok {
			s.processRun(ctx, run)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (s *Service) Enqueue(ctx context.Context, pr domain.PullRequest) (domain.ReviewRun, bool, error) {
	run, created, err := s.store.Claim(ctx, pr)
	if err != nil {
		return run, false, err
	}
	if created {
		s.activeMu.Lock()
		for id, cancel := range s.active {
			old, getErr := s.store.Get(ctx, id)
			if getErr == nil && old.PR.Owner == pr.Owner && old.PR.Repo == pr.Repo && old.PR.Number == pr.Number && old.PR.HeadSHA != pr.HeadSHA {
				cancel()
			}
		}
		s.activeMu.Unlock()
	}
	return run, created, nil
}

func (s *Service) Close() error { return s.store.Close() }

func (s *Service) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("POST /webhooks/github", s.handleGitHubWebhook)
	mux.HandleFunc("GET /runs/{id}", s.handleGetRun)
	return mux
}

func (s *Service) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read webhook body", http.StatusBadRequest)
		return
	}
	if s.config.WebhookSecret == "" || !validSignature(body, r.Header.Get("X-Hub-Signature-256"), s.config.WebhookSecret) {
		http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("X-GitHub-Event") != "pull_request" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var event pullRequestEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid pull request event", http.StatusBadRequest)
		return
	}
	if event.Action != "opened" && event.Action != "reopened" && event.Action != "synchronize" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	pr := domain.PullRequest{Owner: event.Repository.Owner.Login, Repo: event.Repository.Name, Number: event.Number, HeadSHA: event.PullRequest.Head.SHA}
	if pr.Owner == "" || pr.Repo == "" || pr.Number < 1 || pr.HeadSHA == "" {
		http.Error(w, "incomplete pull request event", http.StatusBadRequest)
		return
	}
	if _, _, err := s.Enqueue(r.Context(), pr); err != nil {
		http.Error(w, "enqueue review", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Service) ReviewPR(ctx context.Context, pr domain.PullRequest) error {
	if s.config.GitHubToken == "" {
		return errors.New("GITHUB_TOKEN is required to review a GitHub pull request")
	}
	run, claimed, err := s.store.Claim(ctx, pr)
	if err != nil {
		return err
	}
	if !claimed {
		s.logger.Info("skip duplicate review", "run_id", run.ID, "status", run.Status)
		return nil
	}
	if err := s.store.MarkRunning(ctx, run.ID); err != nil {
		return err
	}
	return s.runReview(ctx, run)
}

func (s *Service) processRun(parent context.Context, run domain.ReviewRun) {
	ctx, cancel := context.WithCancel(parent)
	s.activeMu.Lock()
	s.active[run.ID] = cancel
	s.activeMu.Unlock()
	defer func() { cancel(); s.activeMu.Lock(); delete(s.active, run.ID); s.activeMu.Unlock() }()
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(workerLease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.store.RenewLease(ctx, run.ID, workerLease); err != nil {
					s.logger.Warn("renew task lease", "run_id", run.ID, "error", err)
					cancel()
					return
				}
			}
		}
	}()
	err := s.runReview(ctx, run)
	close(done)
	if err == nil {
		return
	}
	if ctx.Err() != nil {
		return
	}
	if run.Attempts < 3 && retryable(err) {
		delay := time.Second * time.Duration(1<<(run.Attempts-1))
		delay += time.Duration(rand.Int63n(int64(delay/2 + 1)))
		if retryErr := s.store.Retry(context.Background(), run.ID, time.Now().Add(delay), err); retryErr != nil {
			s.logger.Error("schedule review retry", "run_id", run.ID, "error", retryErr)
		}
		return
	}
	if failErr := s.store.Fail(context.Background(), run.ID, err); failErr != nil {
		s.logger.Error("mark review failed", "run_id", run.ID, "error", failErr)
	}
}

func retryable(err error) bool {
	message := strings.ToLower(err.Error())
	for _, code := range []string{"401", "403", "404", "400", "422"} {
		if strings.Contains(message, code) {
			return false
		}
	}
	return true
}

func (s *Service) runReview(ctx context.Context, run domain.ReviewRun) error {
	pr := run.PR
	if s.config.GitHubToken == "" {
		return errors.New("GITHUB_TOKEN is required to review a GitHub pull request")
	}
	files, err := s.github.ChangedFiles(ctx, pr)
	if err != nil {
		return err
	}
	selected, skipped := s.config.Policy.Select(files)
	findings, err := s.reviewFiles(ctx, selected, github.NewContextTools(s.github, pr, files))
	if err != nil {
		return err
	}
	published := 0
	for _, finding := range findings {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.github.CreateComment(ctx, pr, finding); err != nil {
			s.logger.Error("publish finding", "file", finding.File, "line", finding.NewLine, "error", err)
			return err
		}
		published++
	}
	if err := s.github.CreateSummary(ctx, pr, github.Summary(len(selected), published, skipped)); err != nil {
		return fmt.Errorf("publish summary: %w", err)
	}
	status := domain.RunCompleted
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := s.store.Complete(ctx, run.ID, status, len(files), len(selected), published, skipped); err != nil {
		return err
	}
	s.logger.Info("review completed", "run_id", run.ID, "status", status, "files", len(selected), "published", published)
	return nil
}

func (s *Service) handleGetRun(w http.ResponseWriter, r *http.Request) {
	if s.config.AdminToken == "" || r.Header.Get("Authorization") != "Bearer "+s.config.AdminToken {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	run, err := s.store.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Error("get review run", "error", err)
		http.Error(w, "load run", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(run); err != nil {
		s.logger.Error("encode review run", "error", err)
	}
}

func (s *Service) ReviewFixture(ctx context.Context, filename string) error {
	patch, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	file := domain.ChangedFile{Path: "fixture.go", Patch: string(patch)}
	findings, err := s.reviewFiles(ctx, []domain.ChangedFile{file}, nil)
	if err != nil {
		return err
	}
	encoded, _ := json.MarshalIndent(findings, "", "  ")
	fmt.Println(string(encoded))
	return nil
}

func (s *Service) reviewFiles(ctx context.Context, files []domain.ChangedFile, tools review.ToolExecutor) ([]domain.Finding, error) {
	if s.reviewer == nil {
		s.logger.Warn("LLM is not configured; review completed as dry run")
		return nil, nil
	}
	all := make([]domain.Finding, 0)
	reviewer := s.reviewer
	if tools != nil {
		if toolAware, ok := reviewer.(review.ToolAwareReviewer); ok {
			reviewer = toolAware.WithTools(tools)
		}
	}
	for _, file := range files {
		fileContext, cancel := context.WithTimeout(ctx, 60*time.Second)
		candidates, err := reviewer.ReviewFile(fileContext, file)
		cancel()
		if err != nil {
			s.logger.Error("review file", "file", file.Path, "error", err)
			return all, err
		}
		seen := make(map[string]struct{})
		for _, candidate := range candidates {
			finding, ok := s.config.Policy.Validate(file, candidate)
			if !ok {
				s.logger.Debug("finding filtered", "file", file.Path, "title", candidate.Title)
				continue
			}
			key := finding.File + ":" + fmt.Sprint(finding.NewLine) + ":" + strings.ToLower(finding.Title)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			all = append(all, finding)
		}
	}
	return all, nil
}

func validSignature(body []byte, signature, secret string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

type pullRequestEvent struct {
	Action     string `json:"action"`
	Number     int    `json:"number"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	PullRequest struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`
}
