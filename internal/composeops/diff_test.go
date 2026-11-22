package composeops

import (
	"strings"
	"testing"
)

func TestDescribeFileChange(t *testing.T) {
	want := "services:\n  web:\n    image: nginx:1.26\n"
	got := "services:\n  web:\n    image: nginx:1.25\n"

	diff := describeFileChange("/srv/app/compose.yaml", got, want)
	for _, line := range []string{
		"/srv/app/compose.yaml was modified:",
		"--- /srv/app/compose.yaml (host)",
		"+++ /srv/app/compose.yaml (desired)",
		"-    image: nginx:1.25",
		"+    image: nginx:1.26",
	} {
		if !strings.Contains(diff, line) {
			t.Errorf("diff does not contain %q:\n%s", line, diff)
		}
	}
}

func TestDescribeFileChange_truncatesOversizedHunk(t *testing.T) {
	var host, desired strings.Builder
	for i := range maxDiffLines * 2 {
		host.WriteString("old line ")
		host.WriteString(string(rune('a' + i%26)))
		host.WriteByte('\n')
		desired.WriteString("new line ")
		desired.WriteString(string(rune('a' + i%26)))
		desired.WriteByte('\n')
	}

	diff := describeFileChange("compose.yaml", host.String(), desired.String())
	if !strings.Contains(diff, "lines of this changed region omitted") {
		t.Errorf("large diff was not truncated:\n%s", diff)
	}
	if lines := strings.Count(diff, "\n") + 1; lines > maxDiffLines+1 {
		t.Errorf("diff has %d lines, want at most %d", lines, maxDiffLines+1)
	}
}

func TestTruncateUnifiedDiff_keepsWholeHunks(t *testing.T) {
	diff := strings.Join([]string{
		"--- compose.yaml (host)",
		"+++ compose.yaml (desired)",
		"@@ -1,3 +1,3 @@",
		" context",
		"-old one",
		"+new one",
		" context",
		"@@ -100,3 +100,3 @@",
		" context",
		"-old two",
		"+new two",
		" context",
	}, "\n")

	// The header and first hunk fit; the second would cross the limit.
	got := truncateUnifiedDiff(diff, 9, maxDiffBytes)
	for _, want := range []string{
		"@@ -1,3 +1,3 @@",
		"-old one",
		"+new one",
		"1 more changed region omitted",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("diff does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "@@ -100,3 +100,3 @@") {
		t.Errorf("second hunk was not omitted:\n%s", got)
	}
}

func TestTruncateUnifiedDiff_oversizedFirstHunk(t *testing.T) {
	lines := []string{
		"--- compose.yaml (host)",
		"+++ compose.yaml (desired)",
		"@@ -1,20 +1,20 @@",
	}
	for i := range 20 {
		lines = append(lines, "-old line "+string(rune('a'+i)))
	}
	diff := strings.Join(lines, "\n")

	got := truncateUnifiedDiff(diff, 10, maxDiffBytes)
	if !strings.Contains(got, "lines of this changed region omitted") {
		t.Errorf("oversized hunk was not truncated:\n%s", got)
	}
	if lines := strings.Count(got, "\n") + 1; lines > 10 {
		t.Errorf("diff has %d lines, want at most 10", lines)
	}
}

func TestTruncateUnifiedDiff_byteLimit(t *testing.T) {
	diff := strings.Join([]string{
		"--- compose.yaml (host)",
		"+++ compose.yaml (desired)",
		"@@ -1 +1 @@",
		"-" + strings.Repeat("x", 100),
		"+" + strings.Repeat("y", 100),
	}, "\n")

	got := truncateUnifiedDiff(diff, maxDiffLines, 80)
	if !strings.Contains(got, "diff truncated at byte limit") {
		t.Errorf("byte limit was not enforced:\n%s", got)
	}
}
