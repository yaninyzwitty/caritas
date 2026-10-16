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

Seed 10 fictional Kenyan member profiles and a September 2026 financial snapshot
with share history, secured loans, repayments, cash and M-Pesa receipts:

```bash
go run ./scripts/seed
```

Seed identities and contact details are illustrative, not verified real people.
Reruns are idempotent. Existing rows from the older seed are not removed or renamed.

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

## M-Pesa recovery and member credit

Apply `20261015100000_daraja_recovery.sql` and
`20261015110000_credit_operations.sql` with the existing Goose migration process
before starting the updated server. No balances or old payment outcomes are
changed by these migrations.

For local callbacks, tunnel the backend HTTP port and set
`DARAJA_CALLBACK_URL=https://<your-ngrok-host>/webhooks/daraja/stk`, then restart
the backend after changing this setting. Keep the tunnel running while testing.
New prompts append a signed request key to this callback path so callbacks can
restore the checkout association when the initiation response is lost. Forward
the entire `/webhooks/daraja/stk/` path through any proxy. Keep the Daraja passkey
stable while payments are pending; changing it invalidates their callback URLs.

Callbacks are saved before acknowledgement. The server resumes saved callbacks
and queries unresolved checkouts with bounded backoff after restarts. An
initiation timeout remains unconfirmed; retrying a saved request never sends
another prompt. Query success without receipt metadata requires reconciliation
against the M-Pesa business statement; the system never invents a receipt number.

In Contributions → M-Pesa → M-Pesa recovery, cashiers can request a provider
check. Managers can reconcile a missing receipt with its verified M-Pesa
reference, amount, payment time, business-record evidence and reason, or retry
posting after resolving an allocation error. Each received payment retains its
own receipt. Do not enter recovered M-Pesa payments as cash. If an old request
has no checkout ID, obtain the actual checkout ID from the original provider
response or callback before attaching it; do not guess by phone or amount.

Credit is created automatically by loan overpayments. The loan credit panel
shows original, available, reserved and used money. Staff record a retained
member instruction for partial share transfers, eligible loan payments or refund
requests. Refunds reserve funds immediately, require another authorized officer's
approval, and remain reserved until a verified external payout is recorded.
Rejected requests release their reservation. This implementation does not send
refund payouts through Daraja. Credit operations and payment recovery actions
retain their actors, reasons and verification evidence for audit.
