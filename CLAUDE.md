# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is an ISO 8583 message reader and writer library in Go. ISO 8583 is an international standard for card-originated financial transaction messages used by major card networks (Visa, Mastercard, etc.). The library handles message formatting, parsing, and field manipulation with support for various encodings (ASCII, BCD, Binary, EBCDIC).

## Essential Commands

### Development
- `make install` - Tidy and vendor dependencies (run before offline builds)
- `make build` - Compile CLI to `bin/iso8583` with version info
- `make run` - Execute the CLI against local specs
- `go run ./cmd/iso8583` - Run CLI directly

### Testing
- `go test ./...` - Run all tests with coverage
- `make test` - Run test suite, outputs to `cover.out`
- `go test -cover github.com/moov-io/iso8583/...` - Test with coverage display
- `go test -race ./...` - Run tests with race detector
- `go test -run TestSpecificTest` - Run a specific test

### Code Quality
- `make check` - Run linters (gofmt, golangci-lint, gosec) and enforce 75% coverage threshold
- `go fmt ./...` - Format code

### CLI Usage
- `./bin/iso8583 describe msg.bin` - Display ISO8583 message in human-readable format
- `./bin/iso8583 describe -spec spec87ascii msg.bin` - Use specific built-in spec
- `./bin/iso8583 describe -spec-file ./examples/specs/spec87ascii.json msg.bin` - Use custom spec file

## Architecture

### Core Components

**Message Processing Flow**:
```
Building: Set Data → Marshal (struct to fields) → Pack (fields to bytes) → Network
Receiving: Network → Unpack (bytes to fields) → Unmarshal (fields to struct) → Get Data
```

**Key Types** (defined in root package):
- `Message` - Core message type with fields, bitmap, and thread-safe operations (message.go)
- `MessageSpec` - Defines complete message format with field specifications (message_spec.go)

**Field System** (field/ package):
- `field.Field` - Interface for ISO 8583 data elements
- `field.String` - Alphanumeric fields
- `field.Numeric` - Numeric fields
- `field.Binary` - Binary data fields
- `field.Composite` - Structured data (TLV, positional subfields)
- `field.Bitmap` - Special field for presence indicators

**Supporting Packages**:
- `encoding/` - ASCII, BCD, Binary, EBCDIC, BER-TLV encoders
- `prefix/` - Length prefix handling (Fixed, L, LL, LLL, LLLL)
- `padding/` - Field padding (left/right)
- `network/` - Network headers (Binary2Bytes, ASCII4Bytes, BCD2Bytes, VMLH)
- `specs/` - Pre-built message specifications (Spec87ASCII, Spec87Hex, Track2)

### Field Indices Convention
- Index 0: MTI (Message Type Indicator)
- Index 1: Bitmap (presence indicators)
- Index 2+: Data fields
- Bitmap presence bits (1, 65, 129, 193) indicate additional bitmaps but are NOT packed as regular fields

### Message Spec Definition

Each field specification has:
- `Length` - Max length (bytes/characters/digits)
- `Description` - Field purpose
- `Enc` - Encoding type (ASCII, Binary, BCD, LBCD, EBCDIC)
- `Pref` - Prefix encoding and type (Fixed, L, LL, LLL, LLLL)
- `Pad` - Optional padding direction and character

Example specs in `specs/` directory. Most implementations need custom specs - see `/specs` as starting point.

### Struct Tags for Marshal/Unmarshal

Use `iso8583` tags to map struct fields to message fields:
```go
type Authorization struct {
    MTI                  string    `iso8583:"0"`   // MTI
    PrimaryAccountNumber string    `iso8583:"2"`   // PAN
    Amount               int64     `iso8583:"4"`   // Native type support
    AcceptorInfo         *Acceptor `iso8583:"43"`  // Nested structs
}
```

- Tag format: `iso8583:"<field_id>"` or `iso8583:"<field_id>,keepzero"`
- `keepzero` option includes zero-value fields in packed message
- Legacy `index` tag also supported
- Supports anonymous embedded structs
- Native Go types automatically converted

### Thread Safety

All public Message methods (Pack, Unpack, Marshal, Unmarshal) use mutex locks. Internal methods assume mutex is already held.

## Important Implementation Details

### Pack/Unpack/Marshal/Unmarshal Specification

These core operations have been removed and require reimplementation. See `PACK_UNPACK_SPEC.md` for complete specification including:
- Bitmap management and presence bit handling
- Field ordering (always ascending by ID)
- Error wrapping (PackError/UnpackError)
- Embedded struct support in Marshal/Unmarshal
- keepzero tag behavior
- Test coverage requirements (75% minimum)

Key points:
- Bitmap presence bits (65, 129, 193, etc.) are special-cased - NOT packed as data
- Fields must be packed/unpacked in ascending order
- Thread-safe with mutex locking
- Errors include field ID and description for debugging

### Sensitive Data Handling

Use `iso8583.Describe()` to print messages with sensitive fields masked:
- Default: `iso8583.Describe(msg, os.Stdout)` applies `DefaultFilters`
- Custom: `iso8583.Describe(msg, os.Stdout, filterAll(2, filterAll))`
- Unfiltered: `iso8583.Describe(msg, os.Stdout, DoNotFilterFields()...)`

### Network Headers

For network operations, use headers from `network/` package:
- `network.Binary2Bytes` - 2-byte binary length
- `network.ASCII4Bytes` - 4-byte ASCII length
- `network.BCD2Bytes` - 2-byte BCD length
- `network.VMLH` - Visa Message Length Header (2 bytes + 2 reserved)

Companion package `moov-io/iso8583-connection` handles client/server communication.

## Testing Practices

- Tests live beside implementation in `*_test.go` files
- Use table-driven tests for field/encoding variants
- Fixtures in `testdata/` for real message samples
- Run `make check` before submitting PRs (enforces 75% coverage)
- Key test scenarios in PACK_UNPACK_SPEC.md:
  - Basic pack/unpack
  - Three bitmap support (fields beyond 128)
  - Composite fields
  - Embedded structs
  - Concurrent access (race detection)
  - Error handling

## Code Style

- Follow Go Code Review Comments and `gofmt` defaults
- Tabs for indentation, grouped imports, short declarations
- Exported identifiers: descriptive and domain-focused (`MessageSpec`, `BitmapField`)
- Document ISO8583 nuances, not code narration
- Run `go fmt ./...` and `golangci-lint run` before pushing

## Module Information

- Module: `github.com/moov-io/iso8583`
- Go version: 1.23.0+ (supports stable and oldstable per CI)
- Key dependencies: `stretchr/testify`, `yerden/go-util`, `golang.org/x/text`

## Documentation Resources

- `docs/composite-fields.md` - Handling composite fields
- `docs/intro.md` - ISO 8583 introduction
- `docs/mti.md` - Message Type Indicators
- `docs/bitmap.md` - Bitmap structure
- `docs/data-elements.md` - Data field details
- `docs/howtos.md` - Common tasks
- `examples/` - Working code examples
- `PACK_UNPACK_SPEC.md` - Core operations specification
- `AGENTS.md` - Repository contribution guidelines

## Project Structure

```
/cmd/iso8583/          - CLI application
/field/                - Field implementations
/encoding/             - Encoding types
/prefix/               - Length prefix handling
/padding/              - Padding implementations
/network/              - Network header types
/specs/                - Pre-built message specs
/examples/             - Usage examples
/docs/                 - Documentation
/test/                 - Integration tests
/exp/                  - Experimental features
```

## Common Patterns

### Creating and Packing a Message
```go
msg := iso8583.NewMessage(spec)
msg.Marshal(&Authorization{MTI: "0100", ...})
packed, err := msg.Pack()
```

### Unpacking and Reading a Message
```go
msg := iso8583.NewMessage(spec)
err := msg.Unpack(receivedBytes)
var auth Authorization
msg.Unmarshal(&auth)
```

### Defining Custom Specs
Start from examples in `specs/` directory. Most ISO 8583 implementations use confidential specifications, so custom specs are typically required.

## Git Workflow

- Commit messages: short, imperative ("Restore Pack/Unpack implementation")
- Reference issues: `Fixes #123`
- PRs should describe behavior change and testing performed
- CI must pass (tests, linting, 75% coverage)
