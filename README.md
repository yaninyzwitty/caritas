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
export DATABASE_URL='postgres://user:password@localhost:5432/caritas'
./server
```

Configuration comes from environment variables. `DATABASE_URL` is required.
The server defaults to HTTP port `8080` and gRPC port `50051`; set
`HTTP_PORT` and `GRPC_PORT` to override them. In AWS, set these variables on
the container service and supply credentials through its secret injection.
The Docker image runs the server directly and reads the same variables.

| Variable | Default | Purpose |
| --- | --- | --- |
| `DATABASE_URL` | required | PostgreSQL connection URL |
| `HTTP_PORT` | `8080` | HTTP API and webhook listener |
| `GRPC_PORT` | `50051` | gRPC listener |
| `GRPC_TIMEOUT` | `30s` | Daraja HTTP client timeout |
| `DATABASE_MAX_OPEN_CONNS` | `25` | Maximum database connections |
| `DATABASE_MAX_IDLE_CONNS` | `5` | Minimum retained database connections |
| `DATABASE_CONN_MAX_LIFETIME` | `5m` | Database connection lifetime |
| `DATABASE_CONN_MAX_IDLE_TIME` | `1m` | Database connection idle limit |
| `DARAJA_ENABLED` | `false` | Enable Daraja payments |
| `DARAJA_BASE_URL` | Safaricom sandbox URL | Daraja API endpoint |
| `DARAJA_BUSINESS_SHORTCODE` | required when enabled | Daraja shortcode |
| `DARAJA_PASSKEY` | required when enabled | Daraja passkey |
| `DARAJA_CALLBACK_URL` | required when enabled | Public callback URL |
| `DARAJA_CONSUMER_KEY` | required when enabled | Daraja consumer key |
| `DARAJA_CONSUMER_SECRET` | required when enabled | Daraja consumer secret |
| `DARAJA_ACCOUNT_REFERENCE` | `CARITAS` | Payment account reference |
| `DARAJA_TRANSACTION_DESC` | `Caritas contribution` | Payment description |

Durations use Go syntax such as `30s`, `5m`, or `24h`.
The HTTP server is a long-running process; deploy this image to a container
service such as App Runner or ECS/Fargate. Lambda would need an HTTP adapter.

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
- `config/` - Environment variable parsing and defaults

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
