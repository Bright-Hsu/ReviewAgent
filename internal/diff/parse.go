package diff

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var hunkHeader = regexp.MustCompile(`^@@ -(?:(\d+)(?:,\d+)?)? \+(\d+)(?:,\d+)? @@`)

// AddedLines returns line numbers from the new version of a file. It only
// trusts standard unified diff hunks, so comments can never target deleted code.
func AddedLines(patch string) (map[int]string, error) {
	added := make(map[int]string)
	newLine := 0
	inHunk := false

	for _, line := range strings.Split(patch, "\n") {
		if match := hunkHeader.FindStringSubmatch(line); match != nil {
			lineNumber, err := strconv.Atoi(match[2])
			if err != nil {
				return nil, fmt.Errorf("parse hunk header %q: %w", line, err)
			}
			newLine, inHunk = lineNumber, true
			continue
		}
		if !inHunk || line == `\ No newline at end of file` || line == "" {
			continue
		}
		switch line[0] {
		case '+':
			added[newLine] = strings.TrimPrefix(line, "+")
			newLine++
		case ' ':
			newLine++
		case '-':
			// Deleted lines do not advance the new-file cursor.
		}
	}
	return added, nil
}

func LineCount(patch string) int {
	if patch == "" {
		return 0
	}
	return strings.Count(patch, "\n") + 1
}
