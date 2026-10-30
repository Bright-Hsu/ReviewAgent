package domain

import "time"

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

func (s Severity) Valid() bool {
	return s == SeverityCritical || s == SeverityHigh || s == SeverityMedium || s == SeverityLow
}

type ChangedFile struct {
	Path      string
	Status    string
	Patch     string
	Additions int
}

type Finding struct {
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Severity   Severity `json:"severity"`
	Confidence float64  `json:"confidence"`
	File       string   `json:"file"`
	NewLine    int      `json:"new_line"`
	Evidence   []string `json:"evidence"`
	Suggestion string   `json:"suggestion,omitempty"`
}

type PullRequest struct {
	Owner   string
	Repo    string
	Number  int
	HeadSHA string
}

type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunPartial   RunStatus = "partial"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

type ReviewRun struct {
	ID                string
	PR                PullRequest
	Status            RunStatus
	CreatedAt         time.Time
	StartedAt         time.Time
	FinishedAt        time.Time
	Error             string
	FilesSeen         int
	FilesReviewed     int
	FindingsPublished int
	Skipped           []string
	Attempts          int
	LeaseUntil        time.Time
	NextAttemptAt     time.Time
}
