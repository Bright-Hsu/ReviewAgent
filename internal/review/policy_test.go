package review

import (
	"github.com/bright/reviewagent/internal/domain"
	"testing"
)

func TestValidateRejectsDeletedOrUnprovenLine(t *testing.T) {
	p := Policy{MinConfidence: 0.75, MaxFiles: 5, MaxPatchLines: 100}
	file := domain.ChangedFile{Path: "main.go", Patch: "@@ -1 +1 @@\n-old\n+new\n"}
	finding := domain.Finding{Title: "bug", Body: "body", Severity: domain.SeverityHigh, Confidence: .9, File: "main.go", NewLine: 1, Evidence: []string{"new"}}
	if _, ok := p.Validate(file, finding); !ok {
		t.Fatal("expected valid finding")
	}
	finding.NewLine = 2
	if _, ok := p.Validate(file, finding); ok {
		t.Fatal("expected invalid line")
	}
}
