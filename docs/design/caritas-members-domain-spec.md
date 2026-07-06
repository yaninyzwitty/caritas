This domain spec only considers members, not shares / savings, not loans.

**Who is a member**
Change to "A member belongs to a branch. In single-branch mode, all members are assigned to a default branch (ID=1)

**Natural key**
National ID is the dedup key for a member, per branch. Phone and email are mutable contact info only — never used for uniqueness or dedup logic, since both can be reassigned or changed.

**Invariants (must always be true)**

- Default branch ID (1) is constant and must exist for system to operate
- Every member belongs to exactly one branch, permanently. Branch transfer is not supported — if a member needs to move branches, this is out of scope for now and must be handled manually outside the system, not as a domain operation.
- Member number is unique within a branch, sequential, never reused. Counter recovery is monotonic: GREATEST(current_counter, MAX(member_number) + 1), never a blind reset.
- Branch creation/management endpoints are deferred until multi-branch is needed
- Member cannot be hard-deleted, only deactivated via `is_deleted`. A member with an active loan or nonzero share balance cannot be closed — this is enforced at the service layer, not assumed.
- member_profiles is never queried independently — always joined through members, so is_deleted only needs to live on one table.
- No other domain writes to `members` or `member_profiles` directly — must call `member_service`. This is a code convention, not yet an infrastructure-enforced guarantee (single-developer project, enforced via discipline and review for now).
- All writes to `members` and `member_profiles` — not just initial signup — go through `ExecTx`, so a profile update can never partially apply.

**Statuses**

pending -> active
pending -> rejected (terminal)
active -> suspended -> active
active -> closed (terminal, does not reopen)

**What other domains should know (not must)**

- Is member X active in branch Y — yes/no.
- Member ID + branch ID only. Shares and loans domains never receive profile details.

**Failure modes**

- Concurrent member number generation → `FOR UPDATE` on per-branch counter, not `SELECT MAX+1`
- Duplicate signup on retry → unique constraint on national ID per branch + `ON CONFLICT DO NOTHING`
- Soft-deleted member leaks into active queries → default repository method excludes `is_deleted`, no opt-out by accident
- Partial write across `members`/`member_profiles` → wrapped in `ExecTx`, no exceptions
- Stale eligibility read during suspension → re-check status at transaction boundary, not just at request time
- Member data copied into other domains "for convenience" → only member ID + branch ID cross domain boundaries
- `member_service` unavailable → calling workflows (shares/loans) fail fast via activity retry-then-surface, never proceed on an assumed status

**Known gaps / deferred**

- Right to erasure / data retention policy (Kenya Data Protection Act 2019) — not implemented, flagged for later
- RBAC for status transitions — currently restricted to admin role informally, no granular matrix yet
- Audit trail (who changed status, when) — not implemented
- Read replica / cache consistency — not applicable yet, no replicas or cache layer in use
- Multi-branch reporting consistency — deferred until multi-branch is actually built
