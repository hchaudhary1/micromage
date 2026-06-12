# Release And Deployment Guide

Micromage currently ships as a single Go server binary with embedded HTML, JavaScript, CSS, workflow templates, and command prompts. There is no Docker image, package registry, or release automation yet; this guide defines the initial manual release path.

## Prerequisites

- Go version from `go.mod` or newer
- Node.js for the dependency-free browser JavaScript harness tests
- Git
- A clean working tree for release builds
- OpenCode CLI installed on hosts that will run real provider-backed workflows

## Pre-Release Validation

Run the same gates expected by CI:

```sh
go test ./... -cover
go vet ./...
go test -race ./...
for test_file in internal/web/js/*.test.js; do
  node "$test_file"
done
git diff --check
```

Optional local vulnerability check:

```sh
go install golang.org/x/vuln/cmd/govulncheck@latest
"$(go env GOPATH)/bin/govulncheck" ./...
```

Record the commit SHA and validation results in the release notes before publishing binaries.

## Build A Local Binary

Build the host platform binary:

```sh
mkdir -p dist
go build -trimpath -ldflags="-s -w" -o dist/micromage ./cmd/server
```

Run it:

```sh
MICROMAGE_HOST=127.0.0.1 MICROMAGE_PORT=8080 ./dist/micromage
```

The binary contains the embedded web assets from `cmd/server/web`, so deployment does not require copying static files separately.

## Build Release Packages

Build common release targets from a reviewed commit:

```sh
mkdir -p dist

GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/micromage-linux-amd64 ./cmd/server
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/micromage-linux-arm64 ./cmd/server
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/micromage-darwin-arm64 ./cmd/server
GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/micromage-darwin-amd64 ./cmd/server
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/micromage-windows-amd64.exe ./cmd/server
```

Package each binary with the license and operator docs:

```sh
version="v0.0.0"
for target in linux-amd64 linux-arm64 darwin-arm64 darwin-amd64; do
  mkdir -p "dist/micromage-$version-$target"
  cp "dist/micromage-$target" "dist/micromage-$version-$target/micromage"
  cp LICENSE SECURITY.md README.md "dist/micromage-$version-$target/"
  mkdir -p "dist/micromage-$version-$target/docs"
  cp docs/operator-security.md docs/release.md "dist/micromage-$version-$target/docs/"
  tar -C dist -czf "dist/micromage-$version-$target.tar.gz" "micromage-$version-$target"
done

mkdir -p "dist/micromage-$version-windows-amd64"
cp dist/micromage-windows-amd64.exe "dist/micromage-$version-windows-amd64/micromage.exe"
cp LICENSE SECURITY.md README.md "dist/micromage-$version-windows-amd64/"
mkdir -p "dist/micromage-$version-windows-amd64/docs"
cp docs/operator-security.md docs/release.md "dist/micromage-$version-windows-amd64/docs/"
(cd dist && zip -qr "micromage-$version-windows-amd64.zip" "micromage-$version-windows-amd64")
```

Generate checksums:

```sh
(cd dist && shasum -a 256 micromage-*.tar.gz micromage-*.zip > SHA256SUMS)
```

Use the real tag in place of `v0.0.0` once versioned releases begin.

## Deploy On A Host

Install the binary under an unprivileged account:

```sh
install -m 0755 dist/micromage-linux-amd64 /usr/local/bin/micromage
useradd --system --create-home --shell /usr/sbin/nologin micromage
```

Run from the repository or working tree that workflows should operate on:

```sh
cd /srv/micromage/workflows-repo
sudo -u micromage MICROMAGE_HOST=127.0.0.1 MICROMAGE_PORT=8080 /usr/local/bin/micromage
```

Example `systemd` unit:

```ini
[Unit]
Description=Micromage Workflows
After=network.target

[Service]
Type=simple
User=micromage
Group=micromage
WorkingDirectory=/srv/micromage/workflows-repo
Environment=MICROMAGE_HOST=127.0.0.1
Environment=MICROMAGE_PORT=8080
Environment=MICROMAGE_READ_TIMEOUT=15s
Environment=MICROMAGE_READ_HEADER_TIMEOUT=5s
Environment=MICROMAGE_WRITE_TIMEOUT=30m
Environment=MICROMAGE_IDLE_TIMEOUT=60s
Environment=MICROMAGE_SHUTDOWN_TIMEOUT=10s
ExecStart=/usr/local/bin/micromage
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

Add real-run settings only after reading [docs/operator-security.md](operator-security.md):

```ini
Environment=MICROMAGE_ENABLE_REAL_RUNS=1
Environment=MICROMAGE_REAL_RUN_TOKEN=replace-with-a-random-token
```

Prefer an `EnvironmentFile` with restrictive permissions for secrets:

```sh
install -m 0600 -o micromage -g micromage /dev/null /etc/micromage.env
```

Then add this to the unit:

```ini
EnvironmentFile=/etc/micromage.env
```

## Reverse Proxy Guidance

Micromage does not provide built-in multi-user authentication. If remote access is required, keep Micromage bound to loopback and put authentication plus TLS in front of it.

Minimum proxy requirements:

- TLS for all remote traffic
- User authentication before any request reaches Micromage
- WebSocket/SSE-compatible streaming for `/api/run`
- Request body limits at or below Micromage's 1 MiB JSON body limit
- No static exposure of `.micromage/`

## Runtime Configuration

Common environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `MICROMAGE_HOST` | `127.0.0.1` | Bind address |
| `MICROMAGE_PORT` | `8080` | Bind port; takes precedence over `PORT` |
| `PORT` | unset | Fallback bind port for platform hosts |
| `MICROMAGE_ENABLE_REAL_RUNS` | unset | Set to `1` to allow real workflow execution |
| `MICROMAGE_REAL_RUN_TOKEN` | unset | Required bearer token when real runs are enabled |
| `MICROMAGE_BASE_BRANCH` | unset | Optional base branch passed into real bash nodes |
| `MICROMAGE_OPENCODE_UNSAFE` | unset | Set to `1` to append OpenCode's unsafe permission bypass flag |
| `MICROMAGE_READ_TIMEOUT` | `15s` | HTTP read timeout |
| `MICROMAGE_READ_HEADER_TIMEOUT` | `5s` | HTTP read-header timeout |
| `MICROMAGE_WRITE_TIMEOUT` | `30m` | HTTP write timeout for long real-run streams |
| `MICROMAGE_IDLE_TIMEOUT` | `60s` | HTTP idle timeout |
| `MICROMAGE_SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown timeout |

## Release Limitations

- Releases are manually built and attached until a dedicated release workflow exists.
- The binary has no embedded version flag yet; record the Git tag and commit SHA with each package.
- Real-run safety depends on operator environment controls, workflow review, and local tool credentials.
- Durable run history is still evolving; `.micromage/` should be treated as private local state and backed up only when needed.
