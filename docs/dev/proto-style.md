# Protobuf Style Guide

This repository relies on Buf to enforce linting, breaking-change validation,
and code generation for Go and TypeScript targets. A few conventions keep API
packages consistent:

## Naming

- Packages follow semantic versioning. Files live under `proto/<domain>/v1` and
  declare packages such as `nhb.consensus.v1`.
- RPC and message names are `UpperCamelCase` with clear domain prefixes when the
  context is ambiguous.
- Enum values are prefixed with the enum name (e.g. `PROPOSAL_STATUS_VOTING`).

## Fields

- Prefer scalar types and strings for on-chain numeric values that require
  arbitrary precision.
- Use `bytes` for binary hashes and addresses.
- Reserve field numbers rather than reusing them when removing fields.
- Add pagination fields (`page_size`, `page_token`, `next_page_token`) for
  listing RPCs.

## Errors and Status

- gRPC status codes should map to actionable client behaviours. Validation
  issues should return `INVALID_ARGUMENT`; missing resources should return
  `NOT_FOUND`.
- Error details belong in the `message` string for now. When richer errors are
  required we can adopt protobuf error details as a follow-up.

## Pagination

- List RPCs should accept `page_size` and `page_token` fields on the request and
  return a `next_page_token` when more results are available.
- Pagination tokens should be opaque strings so that services can change their
  internal representation without breaking clients.

## Deprecation and Versioning

- Services should bump their package version when a breaking change is
  introduced. Backwards-compatible additions (new fields, new RPCs) can stay on
  the same version.
- Buf's breaking change detection compares against the ref configured via the
  `BUF_BREAKING_AGAINST` environment variable when running `go run
  ./tools/proto/gen.go`.

## Generation

- Run `make proto` after editing `.proto` files. This formats, lints, performs
  optional breaking checks, and regenerates Go/TypeScript code.
- Generated Go code is written adjacent to the source `.proto` files so existing
  import paths continue to work. TypeScript output lands in `clients/ts/`.
