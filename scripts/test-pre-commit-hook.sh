#!/bin/sh
set -eu

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
tmp_root="$(mktemp -d)"
trap 'rm -rf "$tmp_root"' EXIT

bin="$tmp_root/micromage"
git_repo="$tmp_root/repo"

go build -o "$bin" ./cmd/micromage

mkdir "$git_repo"
cd "$git_repo"
git init >/dev/null
git config user.email test@example.com
git config user.name "Test User"
cp "$repo_root/scripts/hooks/pre-commit" .git/hooks/pre-commit
chmod +x .git/hooks/pre-commit

cat > go.mod <<'EOF'
module example.com/precommit

go 1.26.4
EOF
cat > calc.go <<'EOF'
package precommit

func Add(a, b int) int { return a + b }
EOF
cat > calc_test.go <<'EOF'
package precommit

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("bad sum")
	}
}
EOF

git add .
MICROMAGE_BIN="$bin" .git/hooks/pre-commit >/dev/null

printf "%s%s\n" "Generated with " "Claude Code" > note.md
git add note.md
if MICROMAGE_BIN="$bin" .git/hooks/pre-commit >/tmp/micromage-hook.out 2>&1; then
	echo "expected hook to reject banned attribution"
	exit 1
fi
grep -q "banned attribution terms" /tmp/micromage-hook.out

low_repo="$tmp_root/low-coverage-repo"
mkdir "$low_repo"
cd "$low_repo"
git init >/dev/null
git config user.email test@example.com
git config user.name "Test User"
cp "$repo_root/scripts/hooks/pre-commit" .git/hooks/pre-commit
chmod +x .git/hooks/pre-commit

cat > go.mod <<'EOF'
module example.com/lowcoverage

go 1.26.4
EOF
cat > calc.go <<'EOF'
package lowcoverage

func Add(a, b int) int { return a + b }

func Untested() int { return 42 }
EOF
cat > calc_test.go <<'EOF'
package lowcoverage

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("bad sum")
	}
}
EOF

git add .
if MICROMAGE_BIN="$bin" MICROMAGE_COVERAGE_THRESHOLD=90 .git/hooks/pre-commit >/tmp/micromage-hook-coverage.out 2>&1; then
	echo "expected hook to reject low coverage"
	exit 1
fi
grep -q "below required 90.0%" /tmp/micromage-hook-coverage.out

echo "pre-commit hook smoke passed"
