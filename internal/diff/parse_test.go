package diff

import "testing"

func TestAddedLines(t *testing.T) {
	patch := "@@ -4,3 +4,4 @@\n keep\n-old\n+new\n+next\n tail\n"
	lines, err := AddedLines(patch)
	if err != nil {
		t.Fatal(err)
	}
	if lines[5] != "new" || lines[6] != "next" || len(lines) != 2 {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}
