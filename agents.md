## What this is

caritas — a SACCO management system in Go. Production-bound, not a prototype.
Stack: Go, Postgres (pgx/v5, sqlc, goose), Temporal, gRPC, Github Actions.

**IMPORTANT** - Prefer deletion over addition - If something cannot be expressed inline, don't extract it - The IDEAL OUTPUT is the MINIMUM code that correctly implement the described behavior. - Surprise me with LESS not MORE.

## Conventions (with reasons)

- ExecTx wraps every multi-table write — prevents partial-write corruption
  (see docs/design/caritas-members-domain-spec.md, failure mode #4).
- Cursor pagination on (created_at, id) everywhere — offset pagination breaks
  under concurrent writes at scale.
- ON CONFLICT DO NOTHING for idempotency on all retryable inserts.

## Don't

- Don't hard-delete rows, in code or in scripts. is_deleted only.
- Don't add a new query layer — sqlc is the only one.
- Don't bypass member_service to write members/member_profiles directly.

## Domain specs

See docs/design/ — caritas-members-domain-spec.md
Read the relevant one before touching that domain.
