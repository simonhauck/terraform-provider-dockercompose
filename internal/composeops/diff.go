package composeops

import (
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// maxDiffLines bounds how much of a changed file ends up in Terraform state
// and plan output. The start of the diff is enough to explain the drift.
const maxDiffLines = 60

// describeFileChange explains how a mirrored file differs from the spec. It
// returns a unified diff with a short summary line, or a plain "was modified"
// message when no line diff can be produced.
func describeFileChange(path, onHost, desired string) string {
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(onHost),
		B:        difflib.SplitLines(desired),
		FromFile: path + " (host)",
		ToFile:   path + " (desired)",
		Context:  3,
	})
	if err != nil || strings.TrimSpace(diff) == "" {
		return fmt.Sprintf("%s was modified", path)
	}

	lines := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	if len(lines) > maxDiffLines {
		lines = append(lines[:maxDiffLines], "... (diff truncated)")
	}
	return fmt.Sprintf("%s was modified:\n%s", path, strings.Join(lines, "\n"))
}
