package quality

import (
	"strings"
)

// DefaultBannedAttributionTerms blocks tool-signature text from leaking into project history.
var DefaultBannedAttributionTerms = []string{
	bannedTerm("Generated with ", "Claude Code"),
	bannedTerm("Co-Authored-By: ", "Claude"),
	bannedTerm("Co-Authored-By: ", "Codex"),
	bannedTerm("Co-Authored-By: ", "OpenAI"),
	bannedTerm("AI-", "generated"),
	// Block project-specific attribution markers that should not enter commits.
	bannedTerm("arc", "hon"),
	bannedTerm("junho", "yeo"),
	bannedTerm("contra", "bass"),
	bannedTerm("cole", "am00"),
}

func bannedTerm(prefix, suffix string) string {
	return prefix + suffix
}

// AttributionFinding describes a banned term found in staged content.
type AttributionFinding struct {
	Path string
	Term string
	Line int
}

// DetectBannedAttribution finds banned attribution terms in text.
func DetectBannedAttribution(path, content string, bannedTerms []string) []AttributionFinding {
	if len(bannedTerms) == 0 {
		bannedTerms = DefaultBannedAttributionTerms
	}
	var findings []AttributionFinding
	lines := strings.Split(content, "\n")
	for idx, line := range lines {
		for _, term := range bannedTerms {
			if term == "" {
				continue
			}
			if strings.Contains(strings.ToLower(line), strings.ToLower(term)) {
				findings = append(findings, AttributionFinding{Path: path, Term: term, Line: idx + 1})
			}
		}
	}
	return findings
}
