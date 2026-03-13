# Contributing to CareerScout

Thanks for your interest in contributing! Here's how to get started.

## Development Setup

1. Fork and clone the repository
2. Install Go 1.22+, Rust 1.78+, and Python 3.11+
3. Run `docker compose up -d` for local infrastructure
4. Run `psql $DATABASE_URL < schema/001_initial.sql`

## Adding a New ATS Platform

The fastest way to contribute — add support for a new ATS in 3 steps:

### 1. Add the Schema

In `internal/jobparser/parser.go`, add an entry to `KnownSchemas`:

```go
"newats": {
    ATSPlatform:     "newats",
    JobsPath:        "jobs",
    FieldExternalID: "id",
    FieldTitle:      "title",
    FieldLocationRaw: "location.name",
    // ... map all available fields
},
```

### 2. Add the Prober

In `internal/atsprober/probers.go`, add a `ProbeNewATS` function following the pattern of existing probers. Register it in the `Limiters` map and `DomainForATS` switch.

### 3. Add Detection

In `cmd/career_finder/main.go`, add an entry to `atsPatterns` with the domain match string and a slug extraction regex.

## Code Style

- Go: follow `gofmt` and standard Go conventions
- Rust: follow `rustfmt`
- Python: follow PEP 8
- All tools must support graceful shutdown via SIGTERM
- All tools must support checkpoint/resume for crash recovery

## Pull Request Process

1. Create a feature branch from `main`
2. Write tests for new functionality
3. Ensure `go build ./...` passes
4. Submit a PR with a clear description of what and why

## Reporting Issues

Please include:
- Tool name (e.g., `career_finder`, `probe_ats`)
- Input data format
- Expected vs actual behavior
- Relevant log output
