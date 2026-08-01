# Members Domain Specification

This spec owns member identity, member profile data, branch membership, member numbers, member lifecycle status and member status audit.

It does not own shares, savings, loans, contribution receipts, CEEP reporting, dividends or financial balances.

## 1. Domain Boundaries

- Member state is changed only through `member_service`.
- Shares and Loans may ask whether a member is active in a branch. They must not write `members` or `member_profiles` directly.
- Other domains receive member ID and branch ID for decisions. They must not copy profile data for convenience.
- Loans may store member verification references, not mutable profile fields.
- Reports may display member profile fields, but reports are not authoritative member state.

## 2. Core Records

### Branch

- `id`
- `name`
- `status`: Active, Inactive
- `created_at`

In single-branch mode, default branch ID `1` must exist before the system can operate.

### Member

- `id`
- `branch_id`
- `member_number`
- `national_id`
- `status`: Pending, Active, Suspended, WithdrawalPending, Closed, Rejected
- `is_deleted`
- `created_at`
- `updated_at`
- `closed_at`, nullable

### MemberProfile

- `member_id`
- `full_name`
- `phone`, nullable
- `email`, nullable
- `village_or_residence`, nullable
- `created_at`
- `updated_at`

Profile data is mutable. Phone and email are contact fields only and must not be used for deduplication.

### BranchMemberCounter

- `branch_id`
- `next_member_number`
- `updated_at`

The counter is locked when assigning a member number.

### MemberStatusAudit

- `id`
- `member_id`
- `previous_status`
- `new_status`
- `changed_by`
- `reason`
- `created_at`

Status audit entries are append-only.

## 3. Identity and Numbering

Natural key:

```text
branch_id + national_id
```

National ID is the deduplication key per branch. A national ID may not create two live member records in the same branch.

Member number rules:

- unique within a branch
- sequential within a branch
- never reused
- generated only under a `FOR UPDATE` lock on `BranchMemberCounter`
- recovered monotonically with:

```text
next_member_number = greatest(current_counter, max(member_number) + 1)
```

Do not use `SELECT MAX(member_number) + 1` during normal signup.

## 4. Status Model

```text
Pending -> Active
Pending -> Rejected
Active -> Suspended
Suspended -> Active
Active -> WithdrawalPending
Suspended -> WithdrawalPending
WithdrawalPending -> Active
WithdrawalPending -> Closed
Active -> Closed
Suspended -> Closed
```

Rules:

- `Rejected` is terminal.
- `Closed` is terminal. A closed member must not reopen.
- `WithdrawalPending` blocks new loans, disbursements, guarantor commitments, share pledges and new contribution obligations.
- `Suspended` blocks new loans and new share pledges, but existing obligations remain enforceable.
- Direct `Active -> Closed` is allowed only for administrative closure when all external obligations are already clear.
- Membership withdrawal uses `WithdrawalPending -> Closed`. Direct closure is only for administrative closure after all external obligations are already clear.

## 5. Signup

Signup must run in one `ExecTx` and:

- verify default branch exists in single-branch mode
- lock the branch member counter
- insert the member
- insert the member profile
- increment the counter
- write status audit

Duplicate signup retries must be idempotent. Use a unique constraint on `(branch_id, national_id)` and `ON CONFLICT DO NOTHING` or an equivalent idempotent path.

## 6. Profile Updates

All writes touching `members` and `member_profiles` must use `ExecTx`.

Profile updates must not:

- change branch
- change member number
- create a second profile row
- bypass member status checks
- use phone or email for identity matching

## 7. Eligibility Reads

Other domains may ask:

- is member active in branch
- is member suspended
- is member closed or rejected
- is member withdrawal pending
- member ID and branch ID

Eligibility reads used for decisions must be checked at the transaction boundary of the caller. A stale read at request start is not enough.

If `member_service` cannot verify eligibility, the caller must fail fast and retry through the workflow. It must not proceed on assumed status.

## 8. Closure and Withdrawal

Member closure must occur only through `member_service`.

Before a member can close, authoritative checks must confirm:

- no outstanding loan obligations
- no pending loan disbursements
- no active guarantor obligations
- no share balance
- no active share pledges
- no unsettled contribution receipts

Loans owns loan obligations. Shares owns share balance and pledges. Contribution orchestration owns unsettled receipts.

The member service must not calculate financial balances itself. It must request strong, authoritative checks from the owning domains.

When closure succeeds:

- set member status to `Closed`
- set `closed_at`
- write status audit
- prevent future active-member eligibility responses

## 9. Database and Consistency Rules

- All multi-table member writes use `ExecTx`.
- Branch member number generation locks `BranchMemberCounter` with `FOR UPDATE`.
- Use cursor pagination on `(created_at, id)`.
- Do not hard-delete members or profiles.
- Default member queries exclude `is_deleted`.
- `member_profiles` is not queried independently. It is joined through `members`.
- Status changes write `MemberStatusAudit`.

Required database protections:

- default branch exists in single-branch mode
- unique `(branch_id, national_id)` for non-deleted members
- unique `(branch_id, member_number)`
- one profile per member
- valid status transitions enforced by service logic

## 10. Failure Modes

### Signup and Identity

- Duplicate signup succeeds after retry.
- Same national ID creates two live members in the same branch.
- Phone or email is used as the deduplication key.
- Concurrent signups receive the same member number.
- Counter is reset below an already-issued member number.
- Member is created without a profile or profile is created without member.

### Status and Eligibility

- Suspended member is treated as active.
- Closed or rejected member is treated as active.
- Member enters withdrawal flow but still receives a new loan, pledge or contribution obligation.
- Stale member status is used during loan approval or disbursement.
- Status changes are made without audit.
- Unauthorized user changes member status.

### Cross-Domain Closure

- Member closes while loan obligations still exist.
- Member closes while share balance is nonzero.
- Member closes while shares are pledged or guarantor obligations are active.
- Member service calculates financial balances itself instead of asking owning domains.
- External service is unavailable and closure proceeds on assumed values.
- Closure succeeds in Members but share account remains active.

### Data Boundaries

- Loans or Shares writes directly to `members` or `member_profiles`.
- Other domains copy mutable profile fields for convenience.
- Reports use copied member profile data that has gone stale.
- Soft-deleted member leaks into active queries.
- Branch transfer is attempted even though it is unsupported.

### System-Wide

- Multi-table member write partially applies.
- Records are hard-deleted instead of statused or soft-deleted.
- List endpoints use offset pagination and behave incorrectly under concurrent writes.
- Imported member rows contain duplicate national IDs or malformed profile data.

## 11. Resolved Inconsistencies

These were fixed while rewriting this spec:

- Added `WithdrawalPending` because the cross-domain withdrawal workflow depends on it.
- Added `MemberStatusAudit`; the old spec listed audit as a gap even though status transitions are business-critical.
- Made closure checks explicitly call Loans, Shares and contribution orchestration instead of implying Members owns financial checks.
- Clarified that direct writes to `members` and `member_profiles` are forbidden for other domains.
- Kept national ID as the natural key per branch and kept phone/email mutable.
- Kept branch transfer unsupported and made it explicit in failure modes.
- Replaced vague "active yes/no" reads with explicit eligibility responses other domains may request.

## 12. Open Decisions

These must be settled before implementation:

- Is `WithdrawalPending -> Active` allowed when a withdrawal request is cancelled, and who can approve it?
- Should `Suspended -> Closed` be allowed directly, or must all closure pass through `WithdrawalPending`?
- What exact RBAC roles can approve activation, suspension, withdrawal cancellation and closure?
- Should `is_deleted` remain separate from `Closed`, or should closed members rely only on terminal status?
- What data-retention policy applies to member profile fields under Kenya Data Protection Act 2019?
- What exact branch ID type should be used long term: integer default branch IDs or UUIDs?
