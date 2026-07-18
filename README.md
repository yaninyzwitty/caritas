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
