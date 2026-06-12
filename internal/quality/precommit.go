package quality

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const DefaultCoverageThreshold = 70.0

// PreCommitOptions describes the repository quality gate enforced by pre-commit.
type PreCommitOptions struct {
	Repo              string
	CoverageThreshold float64
	BannedTerms       []string
}

// PreCommitResult records the evidence that a local commit satisfied or failed the gate.
type PreCommitResult struct {
	Coverage CoverageSummary
	Findings []AttributionFinding
}

// RunPreCommit enforces attribution hygiene and the Go coverage floor for staged changes.
func RunPreCommit(ctx context.Context, opts PreCommitOptions) (PreCommitResult, error) {
	if opts.Repo == "" {
		opts.Repo = "."
	}
	if opts.CoverageThreshold == 0 {
		opts.CoverageThreshold = DefaultCoverageThreshold
	}

	var result PreCommitResult
	staged, err := loadStagedFiles(ctx, opts.Repo)
	if err != nil {
		return result, err
	}
	for _, file := range staged {
		result.Findings = append(result.Findings, DetectBannedAttribution(file.Path, file.Content, opts.BannedTerms)...)
	}
	if len(result.Findings) > 0 {
		return result, fmt.Errorf("banned attribution terms found")
	}

	coverage, err := runCoverage(ctx, opts.Repo)
	if err != nil {
		return result, err
	}
	result.Coverage = coverage
	if coverage.Percent+0.00001 < opts.CoverageThreshold {
		return result, fmt.Errorf("coverage %.1f%% is below required %.1f%%", coverage.Percent, opts.CoverageThreshold)
	}
	return result, nil
}

func runCoverage(ctx context.Context, repo string) (CoverageSummary, error) {
	dir, err := os.MkdirTemp("", "micromage-coverage-*")
	if err != nil {
		return CoverageSummary{}, err
	}
	defer os.RemoveAll(dir)

	profile := filepath.Join(dir, "coverage.out")
	cmd := exec.CommandContext(ctx, "go", "test", "./...", "-coverprofile="+profile)
	cmd.Dir = repo
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return CoverageSummary{}, fmt.Errorf("go test coverage failed: %w\n%s", err, strings.TrimSpace(output.String()))
	}

	f, err := os.Open(profile)
	if err != nil {
		return CoverageSummary{}, err
	}
	defer f.Close()
	return ParseCoverProfile(f)
}

// FormatPreCommitResult gives hook users a concise, actionable quality report.
func FormatPreCommitResult(result PreCommitResult, err error) string {
	var b strings.Builder
	if len(result.Findings) > 0 {
		b.WriteString("banned attribution terms:\n")
		for _, finding := range result.Findings {
			fmt.Fprintf(&b, "  %s:%d contains %q\n", finding.Path, finding.Line, finding.Term)
		}
	}
	if result.Coverage.TotalStatements > 0 {
		fmt.Fprintf(&b, "coverage: %.1f%% (%d/%d statements)\n", result.Coverage.Percent, result.Coverage.CoveredStatements, result.Coverage.TotalStatements)
	}
	if err != nil {
		fmt.Fprintf(&b, "quality gate failed: %v\n", err)
	} else {
		b.WriteString("quality gate passed\n")
	}
	return b.String()
}
