package skills

import (
	"fmt"
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	tests := []struct {
		name     string
		old, new string
		want     string
	}{
		{"identical", "a\nb\n", "a\nb\n", ""},
		{"empty", "", "", ""},
		{"create", "", "one\ntwo\n", "@@ -0,0 +1,2 @@\n+one\n+two\n"},
		{"delete", "one\ntwo\n", "", "@@ -1,2 +0,0 @@\n-one\n-two\n"},
		{"modify", "a\nb\nc\n", "a\nx\nc\n", "@@ -1,3 +1,3 @@\n a\n-b\n+x\n c\n"},
		{"removal without replacement", "a\nb\nc\nd\n", "a\nc\nd\n", "@@ -1,4 +1,3 @@\n a\n-b\n c\n d\n"},
		{"no trailing newline is ignored", "a\nb", "a\nc", "@@ -1,2 +1,2 @@\n a\n-b\n+c\n"},
	}
	for _, tt := range tests {
		if got := unifiedDiff(tt.old, tt.new); got != tt.want {
			t.Errorf("%s:\ngot:\n%s\nwant:\n%s", tt.name, got, tt.want)
		}
	}
}

func TestUnifiedDiffContext(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i+1)
	}
	old := strings.Join(lines, "\n") + "\n"
	lines[9] = "changed"
	newText := strings.Join(lines, "\n") + "\n"

	got := unifiedDiff(old, newText)
	if !strings.HasPrefix(got, "@@ -7,7 +7,7 @@\n") {
		t.Fatalf("hunk header:\n%s", got)
	}
	for _, want := range []string{" l7\n", " l8\n", " l9\n", "-l10\n", "+changed\n", " l11\n", " l12\n", " l13\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff misses %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{" l6\n", " l14\n"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("diff has too much context %q:\n%s", unwanted, got)
		}
	}
}

func TestUnifiedDiffHunkGrouping(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i+1)
	}
	old := strings.Join(lines, "\n") + "\n"

	far := append([]string(nil), lines...)
	far[2] = "x"
	far[24] = "y"
	if got := unifiedDiff(old, strings.Join(far, "\n")+"\n"); strings.Count(got, "@@ ") != 2 {
		t.Errorf("changes far apart should give two hunks:\n%s", got)
	}

	closeBy := append([]string(nil), lines...)
	closeBy[9] = "x"
	closeBy[12] = "y"
	if got := unifiedDiff(old, strings.Join(closeBy, "\n")+"\n"); strings.Count(got, "@@ ") != 1 {
		t.Errorf("close changes should give one hunk:\n%s", got)
	}
}

// diffOps falls back to remove-all/add-all for very large inputs; the result
// must still be a valid diff and must not allocate a huge LCS table.
func TestUnifiedDiffLargeInput(t *testing.T) {
	var old, newText strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&old, "old-%d\n", i)
		fmt.Fprintf(&newText, "new-%d\n", i)
	}
	got := unifiedDiff(old.String(), newText.String())
	if got == "" || !strings.HasPrefix(got, "@@ ") {
		t.Fatalf("large diff is empty or malformed:\n%.200s", got)
	}
	if strings.Count(got, "-old-") != 2000 || strings.Count(got, "+new-") != 2000 {
		t.Fatalf("large diff lost lines (%d removed, %d added)",
			strings.Count(got, "-old-"), strings.Count(got, "+new-"))
	}
}
