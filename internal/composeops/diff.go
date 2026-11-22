package composeops

import (
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

const (
	// maxDiffLines and maxDiffBytes bound how much of a changed file ends up in
	// Terraform state and plan output. Whole hunks are kept until the line
	// budget is exhausted; the byte budget is a safety net for pathological
	// lines and rarely applies to compose files.
	maxDiffLines = 200
	maxDiffBytes = 16 << 10
)

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
	return fmt.Sprintf("%s was modified:\n%s", path, truncateUnifiedDiff(diff, maxDiffLines, maxDiffBytes))
}

// truncateUnifiedDiff keeps complete hunks until the line budget is reached.
// If a single hunk is larger than the whole budget, it keeps the start of that
// hunk and says how much was omitted. The final byte cap protects against
// extremely long lines and may cut the rendered diff.
func truncateUnifiedDiff(diff string, maxLines, maxBytes int) string {
	lines := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	header := make([]string, 0, 2)
	var hunks [][]string
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "@@"):
			hunks = append(hunks, []string{line})
		case len(hunks) == 0:
			header = append(header, line)
		default:
			hunks[len(hunks)-1] = append(hunks[len(hunks)-1], line)
		}
	}
	if len(hunks) == 0 {
		return strings.Join(header, "\n")
	}

	out := append([]string{}, header...)
	used := len(header)
	included := 0
	for _, hunk := range hunks {
		if used+len(hunk) > maxLines {
			break
		}
		out = append(out, hunk...)
		used += len(hunk)
		included++
	}

	if included == 0 {
		// One changed region is larger than the entire budget. Keep its start
		// rather than hiding every changed line.
		first := hunks[0]
		room := maxLines - used - 1 // reserve the summary line
		if room < 0 {
			room = 0
		}
		if room > len(first) {
			room = len(first)
		}
		out = append(out, first[:room]...)
		omittedLines := len(first) - room
		omittedHunks := len(hunks) - 1
		summary := fmt.Sprintf("... (%d lines of this changed region omitted", omittedLines)
		if omittedHunks > 0 {
			summary += fmt.Sprintf("; %d more changed region%s omitted", omittedHunks, plural(omittedHunks))
		}
		out = append(out, summary+")")
	} else if omitted := len(hunks) - included; omitted > 0 {
		out = append(out, fmt.Sprintf("... (%d more changed region%s omitted)", omitted, plural(omitted)))
	}

	result := strings.Join(out, "\n")
	if len(result) > maxBytes {
		cut := strings.LastIndex(result[:maxBytes], "\n")
		if cut <= 0 {
			cut = maxBytes
		}
		result = result[:cut] + "\n... (diff truncated at byte limit)"
	}
	return result
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
