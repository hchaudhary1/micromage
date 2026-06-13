package quality

import "testing"

func TestDetectBannedAttributionReportsPathLineAndTerm(t *testing.T) {
	term := bannedTerm("Generated with ", "Claude Code")
	findings := DetectBannedAttribution("README.md", "normal\n"+term+"\n", nil)
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(findings))
	}
	if findings[0].Path != "README.md" || findings[0].Line != 2 || findings[0].Term != term {
		t.Fatalf("unexpected finding: %+v", findings[0])
	}
}

func TestDetectBannedAttributionIsCaseInsensitive(t *testing.T) {
	findings := DetectBannedAttribution("commit.txt", "co-authored-by: "+"codex\n", nil)
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(findings))
	}
}

func TestDetectBannedAttributionIncludesProjectSpecificTerms(t *testing.T) {
	for _, term := range []string{
		bannedTerm("arc", "hon"),
		bannedTerm("junho", "yeo"),
		bannedTerm("contra", "bass"),
		bannedTerm("cole", "am00"),
	} {
		t.Run(term, func(t *testing.T) {
			findings := DetectBannedAttribution("note.md", "mentions "+term+"\n", nil)
			if len(findings) != 1 {
				t.Fatalf("got %d findings, want 1", len(findings))
			}
			if findings[0].Term != term {
				t.Fatalf("got term %q, want %q", findings[0].Term, term)
			}
		})
	}
}
