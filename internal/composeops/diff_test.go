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

func TestDescribeFileChange_truncates(t *testing.T) {
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
	if !strings.Contains(diff, "... (diff truncated)") {
		t.Errorf("large diff was not truncated:\n%s", diff)
	}
	if lines := strings.Count(diff, "\n"); lines > maxDiffLines+2 {
		t.Errorf("diff has %d lines, want at most %d", lines, maxDiffLines+2)
	}
}
