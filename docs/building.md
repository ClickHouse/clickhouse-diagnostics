# Building from source

*Moved from the main [README](../README.md), which keeps a short version of each section.*

## Installation

### Prerequisites

- Go 1.23 or later (the module declares `go 1.23.9`)
- `make` (optional — convenience targets only; everything also works with plain `go` commands)
- Network access to a ClickHouse HTTP(S) interface
- Read access to the ClickHouse config directory (optional, only if collecting configs)

### Build with Make (recommended)

The repo ships with a `Makefile` that wraps the common workflows. From the repo root:

```bash
make deps        # go mod download && go mod tidy
make build       # produces ./bin/clickhouse-diagnostic
make run         # builds (if needed) and runs the binary interactively
make test        # go test -v ./...
make fmt         # go fmt ./...
make lint        # golangci-lint run  (requires golangci-lint installed)
make clean       # remove ./bin, ./clickhouse_results, ./configuration, *.tar.gz
make help        # list every target
```

### Build with `go` directly

If you don't want to use `make`:

```bash
# Fetch and tidy dependencies
go mod download
go mod tidy

# Build a binary at ./clickhouse-diagnostic
go build -o clickhouse-diagnostic ./cmd

# Or install into $GOPATH/bin (or $GOBIN) so it's on your PATH
go install ./cmd
```

### Cross-platform release builds

`make release` produces binaries for the platforms we ship:

```bash
make release
# Output (in ./bin/):
#   clickhouse-diagnostic-linux-amd64
#   clickhouse-diagnostic-darwin-amd64
#   clickhouse-diagnostic-darwin-arm64
#   clickhouse-diagnostic-windows-amd64.exe
```

To cross-compile for a single target without `make`:

```bash
GOOS=linux  GOARCH=amd64 go build -o bin/clickhouse-diagnostic-linux-amd64  ./cmd
GOOS=darwin GOARCH=arm64 go build -o bin/clickhouse-diagnostic-darwin-arm64 ./cmd
```

Builds are statically linked Go binaries — copy the file to the target host and run, no runtime dependencies required.
