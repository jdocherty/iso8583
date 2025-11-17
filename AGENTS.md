# Repository Guidelines

## Project Structure & Module Organization
iso8583 is a Go module rooted at the repo root, exposing reusable packages grouped by concern. Message mechanics live in `message.go`, `message_spec.go`, and supporting folders such as `field/`, `encoding/`, `padding/`, `prefix/`, and `network/`. Ready-made message definitions are in `specs/` with ASCII variants like `spec87ascii.json`, while `examples/` and `docs/` illustrate how to compose specifications and flows. The CLI lives under `cmd/iso8583`, assembling into `bin/iso8583`. Tests reside next to their packages in `*_test.go` files, with integration data under `test/` and `testdata/`. Use `exp/` for experimental helpers and keep assets (logos, coverage) in the existing directories to avoid bloating the root.

## Build, Test, and Development Commands
- `make install`: tidy and vendor dependencies; run before offline builds.  
- `make build`: compile the CLI with embedded version info to `bin/iso8583`.  
- `make run` or `go run ./cmd/iso8583`: execute the CLI against local specs.  
- `go test ./...` or `make test`: run the suite with coverage output in `cover.out`.  
- `make check`: downloads the canonical lint script and enforces `gofmt`, `golangci-lint`, `gosec`, and 75% coverage.

## Coding Style & Naming Conventions
Follow the Go Code Review Comments and `gofmt` defaults (tabs for indentation, grouped imports, short declarations). Keep exported identifiers descriptive and domain-focused (`MessageSpec`, `BitmapField`), and reserve unexported helpers for internal logic. Struct tags must use the `iso8583:"<field>"` syntax shown in `examples/`. Run `go fmt ./...` and `golangci-lint run` (installed by `make check`) before pushing. Document tricky bits with short comments explaining ISO8583 nuances rather than code narration.

## Testing Guidelines
Add unit tests beside the implementation and name them `Test<Thing>` or `Benchmark<Thing>` following Go conventions. Use fixtures from `testdata/` to cover real message samples, and prefer table-driven tests for new fields or encodings. Every PR should run `make check`; CI enforces a minimum 75% statement coverage, so extend existing suites when touching foundational files like `message.go` or `field_filter.go`. Update `cover.out` only via tooling—do not hand-edit.

## Commit & Pull Request Guidelines
Commit messages are short, imperative statements (see history entries like `Restore Pack/Unpack...` and `fix(deps): update module ...`). Group related changes per commit and reference issues with `Fixes #123` when relevant. PRs should describe the behavior change, testing performed (`make check`, manual CLI runs), and attach sample payloads or screenshots for CLI UX tweaks. Link specs or docs you touched, ensure CI passes, and call out any follow-up work so maintainers can review efficiently.
