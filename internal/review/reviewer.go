package review

import (
	"context"
	"encoding/json"

	"github.com/bright/reviewagent/internal/domain"
)

// Reviewer supplies candidate findings only. Publishing and validation stay in
// the application layer so a model can never post arbitrary GitHub comments.
type Reviewer interface {
	ReviewFile(ctx context.Context, file domain.ChangedFile) ([]domain.Finding, error)
}

// ToolExecutor is intentionally small: reviewers cannot execute commands or
// make arbitrary HTTP calls. New tool names must be added deliberately.
type ToolExecutor interface {
	Call(ctx context.Context, name string, arguments json.RawMessage) (string, error)
}

type ToolAwareReviewer interface {
	Reviewer
	WithTools(executor ToolExecutor) Reviewer
}
