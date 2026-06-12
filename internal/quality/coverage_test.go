package quality

import (
	"strings"
	"testing"
)

func TestParseCoverProfileComputesGlobalCoverage(t *testing.T) {
	profile := `mode: set
github.com/hchaudhary1/micromage/a.go:1.1,2.2 3 1
github.com/hchaudhary1/micromage/b.go:3.1,4.2 7 0
`

	got, err := ParseCoverProfile(strings.NewReader(profile))
	if err != nil {
		t.Fatal(err)
	}
	if got.CoveredStatements != 3 || got.TotalStatements != 10 {
		t.Fatalf("got covered=%d total=%d, want 3/10", got.CoveredStatements, got.TotalStatements)
	}
	if got.Percent != 30 {
		t.Fatalf("got percent %.2f, want 30.00", got.Percent)
	}
}

func TestParseCoverProfileRejectsEmptyProfiles(t *testing.T) {
	_, err := ParseCoverProfile(strings.NewReader("mode: atomic\n"))
	if err == nil {
		t.Fatal("expected empty profile error")
	}
}
