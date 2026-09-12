# Caritas

SACCO management system in Go, Postgres, Temporal, gRPC.

## Stack

- Go 1.26.1
- Postgres (pgx/v5, sqlc, goose)
- Temporal
- gRPC

## Prerequisites

- Go 1.26.1
- Temporal server running
- Postgres

## Quick Start

```bash
go mod download
go build ./cmd/server
./server
```

The server exposes gRPC on the configured gRPC port and generated HTTP/JSON
routes on the configured HTTP port. Better Auth JWTs are accepted through an
`Authorization: Bearer <token>` header on either transport.

Generated HTTP routes use `/api/v1/<group>/<operation>`. For example:

```http
POST /api/v1/members/get
Authorization: Bearer <access_token>
Content-Type: application/json

{"member_id":"00000000-0000-0000-0000-000000000000"}
```

## Project Structure

- `cmd/` - Main applications
- `internal/` - Domain logic (member, share, loan)
- `proto/` - gRPC service definitions
- `migrations/` - Database migrations (goose)
- `config.yaml` - Configuration

## Key Conventions

- ExecTx wraps every multi-table write (prevents partial-write corruption)
- Cursor pagination on (created_at, id) everywhere
- ON CONFLICT DO NOTHING for idempotency on retryable inserts
- Soft deletes only (is_deleted flag)

## Domain Specs

See `docs/design/`:

- caritas-members-domain-spec.md
- caritas-shares-domain-spec.md
- caritas-loans-domain-spec.md

Read the relevant spec before touching a domain.
