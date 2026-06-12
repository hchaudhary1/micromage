package quality

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// CoverageSummary captures the project-level signal the hook uses to protect the coverage floor.
type CoverageSummary struct {
	CoveredStatements int
	TotalStatements   int
	Percent           float64
}

// ParseCoverProfile computes global Go statement coverage from a coverprofile.
func ParseCoverProfile(r io.Reader) (CoverageSummary, error) {
	scanner := bufio.NewScanner(r)
	lineNo := 0
	var covered, total int
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "mode:") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) != 3 {
			return CoverageSummary{}, fmt.Errorf("coverage line %d: expected 3 fields", lineNo)
		}
		statements, err := strconv.Atoi(fields[1])
		if err != nil {
			return CoverageSummary{}, fmt.Errorf("coverage line %d: parse statements: %w", lineNo, err)
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			return CoverageSummary{}, fmt.Errorf("coverage line %d: parse count: %w", lineNo, err)
		}
		total += statements
		if count > 0 {
			covered += statements
		}
	}
	if err := scanner.Err(); err != nil {
		return CoverageSummary{}, err
	}
	if total == 0 {
		return CoverageSummary{}, fmt.Errorf("coverage profile has no statements")
	}
	return CoverageSummary{
		CoveredStatements: covered,
		TotalStatements:   total,
		Percent:           float64(covered) * 100 / float64(total),
	}, nil
}
