package review

import (
	"path"
	"strings"

	"github.com/bright/reviewagent/internal/diff"
	"github.com/bright/reviewagent/internal/domain"
)

type Policy struct {
	MinConfidence float64
	MaxFiles      int
	MaxPatchLines int
}

func (p Policy) Select(files []domain.ChangedFile) (selected []domain.ChangedFile, skipped []string) {
	for _, file := range files {
		if len(selected) >= p.MaxFiles {
			skipped = append(skipped, "reached max_files limit")
			break
		}
		if ignored(file.Path) {
			skipped = append(skipped, "ignored "+file.Path)
			continue
		}
		if file.Patch == "" || diff.LineCount(file.Patch) > p.MaxPatchLines {
			skipped = append(skipped, "patch unavailable or too large: "+file.Path)
			continue
		}
		selected = append(selected, file)
	}
	return selected, skipped
}

func (p Policy) Validate(file domain.ChangedFile, candidate domain.Finding) (domain.Finding, bool) {
	if candidate.File != file.Path || candidate.Title == "" || candidate.Body == "" || len(candidate.Evidence) == 0 || !candidate.Severity.Valid() || candidate.Confidence < p.MinConfidence {
		return domain.Finding{}, false
	}
	added, err := diff.AddedLines(file.Patch)
	if err != nil || added[candidate.NewLine] == "" {
		return domain.Finding{}, false
	}
	return candidate, true
}

func ignored(filename string) bool {
	base := path.Base(filename)
	if strings.HasSuffix(base, ".lock") || strings.HasSuffix(base, ".min.js") {
		return true
	}
	for _, prefix := range []string{"vendor/", "node_modules/", "dist/", "coverage/"} {
		if strings.HasPrefix(filename, prefix) {
			return true
		}
	}
	return false
}
