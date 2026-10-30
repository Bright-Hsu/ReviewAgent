package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bright/reviewagent/internal/domain"
)

func TestSQLiteStoreRunLifecycleAndIdempotency(t *testing.T) {
	s, err := OpenSQLite("file:review-run-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pr := domain.PullRequest{Owner: "acme", Repo: "orders", Number: 42, HeadSHA: "abc"}
	run, claimed, err := s.Claim(context.Background(), pr)
	if err != nil || !claimed || run.Status != domain.RunQueued {
		t.Fatalf("run=%+v claimed=%v err=%v", run, claimed, err)
	}
	if err := s.MarkRunning(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(context.Background(), run.ID, domain.RunCompleted, 4, 3, 2, []string{"ignored vendor/file.go"}); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Get(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.RunCompleted || stored.FilesSeen != 4 || stored.FilesReviewed != 3 || stored.FindingsPublished != 2 || len(stored.Skipped) != 1 || stored.FinishedAt.IsZero() {
		t.Fatalf("unexpected persisted run: %+v", stored)
	}
	existing, claimed, err := s.Claim(context.Background(), pr)
	if err != nil || claimed || existing.ID != run.ID {
		t.Fatalf("existing=%+v claimed=%v err=%v", existing, claimed, err)
	}
}

func TestQueueLeaseRetryRecoveryAndCancellation(t *testing.T) {
	s, err := OpenSQLite("file:queue-recovery-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	oldPR := domain.PullRequest{Owner: "acme", Repo: "orders", Number: 7, HeadSHA: "old"}
	old, created, err := s.Claim(ctx, oldPR)
	if err != nil || !created {
		t.Fatalf("claim old: %v %v", created, err)
	}
	run, ok, err := s.ClaimNext(ctx, time.Minute)
	if err != nil || !ok || run.Attempts != 1 {
		t.Fatalf("lease=%+v ok=%v err=%v", run, ok, err)
	}
	if err := s.RenewLease(ctx, run.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.Retry(ctx, run.ID, time.Now().Add(-time.Second), errors.New("temporary")); err != nil {
		t.Fatal(err)
	}
	run, ok, err = s.ClaimNext(ctx, time.Minute)
	if err != nil || !ok || run.Attempts != 2 {
		t.Fatalf("second lease=%+v ok=%v err=%v", run, ok, err)
	}
	if err := s.RecoverExpired(ctx, time.Now().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Get(ctx, run.ID)
	if err != nil || stored.Status != domain.RunQueued {
		t.Fatalf("recovered=%+v err=%v", stored, err)
	}
	newPR := oldPR
	newPR.HeadSHA = "new"
	if _, created, err := s.Claim(ctx, newPR); err != nil || !created {
		t.Fatalf("claim new: %v %v", created, err)
	}
	stored, err = s.Get(ctx, old.ID)
	if err != nil || stored.Status != domain.RunCancelled {
		t.Fatalf("old run=%+v err=%v", stored, err)
	}
}

func TestSQLiteStoreRejectsInvalidTransition(t *testing.T) {
	s, err := OpenSQLite("file:review-run-transition-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	run, _, err := s.Claim(context.Background(), domain.PullRequest{Owner: "acme", Repo: "orders", Number: 1, HeadSHA: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(context.Background(), run.ID, domain.RunCompleted, 0, 0, 0, nil); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("expected invalid transition, got %v", err)
	}
	if err := s.Fail(context.Background(), run.ID, errors.New("GitHub unavailable")); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Get(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.RunFailed || stored.Error != "GitHub unavailable" {
		t.Fatalf("unexpected failed run: %+v", stored)
	}
}
