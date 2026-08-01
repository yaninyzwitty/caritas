# Loans Domain Specification

This spec owns loan applications, approvals, disbursements, repayment schedules, repayments, loan interest, penalties, overpayment credit, guarantor commitments and loan audit history.

It does not own member identity, member lifecycle, share balances, share pledges or contribution reporting.

## 1. Domain Boundaries

- Member status and identity are owned by `member_service`.
- Shares, share balances, share contribution history and pledged shares are owned by the Shares domain.
- Contributions and CEEP reporting are coordinated by the cross-domain contribution spec.
- The Loans domain may read member eligibility and share security with strong consistency. It must not write member or share state directly.
- Loan decisions must store member ID, branch ID and verification references. Do not copy member profile details into Loans except as legally required immutable audit snapshots.

## 2. Core Records

Use decimal money types only. Do not use floats.

### LoanApplication

- `id`
- `member_id`
- `branch_id`
- `requested_amount`
- `purpose`
- `declared_monthly_income`, if required for review
- `status`: Draft, Pending, Approved, Rejected, Cancelled, Expired, ManualReview
- `member_verification_ref`
- `share_verification_ref`
- `created_at`
- `updated_at`

### Loan

- `id`
- `application_id`
- `member_id`
- `branch_id`
- `approved_principal`
- `disbursed_principal`
- `interest_rate`
- `repayment_period_months`
- `status`: Approved, Disbursed, Active, Delinquent, Restructuring, ManualReview, Closed, WrittenOff
- `approved_at`
- `disbursed_at`
- `closed_at`
- `created_at`
- `updated_at`
- `updated_by`

### RepaymentSchedule

- `id`
- `loan_id`
- `installment_no`
- `due_date`
- `principal_due`
- `interest_due`
- `penalty_due`
- `status`: Upcoming, Due, Partial, Paid, Missed, Superseded

Schedules are append-only after creation. Restructuring supersedes old rows and creates new rows.

### LoanTransaction

Loan transactions are append-only. They are never updated or deleted.

- `id`
- `loan_id`
- `type`: Disbursement, Repayment, Penalty, Reversal, CreditCreated, CreditWithdrawal, Adjustment
- `amount`
- `reference_id`
- `payment_gateway_transaction_id`, when from an external payment source
- `reversal_of`, nullable
- `allocation_breakdown`
- `created_at`
- `created_by`

### LoanGuarantor

- `id`
- `loan_id`
- `guarantor_member_id`
- `guaranteed_amount`
- `status`: Pending, Approved, Rejected, Released
- `approved_at`
- `approved_by`
- `released_at`
- `created_at`

### CreditBalance

Overpayments are tracked separately from loan principal.

- `id`
- `member_id`
- `loan_id`, nullable
- `amount`
- `source`: Overpayment, Refund, Adjustment
- `status`: Available, Frozen, Withdrawn
- `created_at`
- `last_activity_at`

### LoanAuditTrail

Audit entries are append-only.

- `id`
- `loan_id`
- `event_type`
- `field_changed`, nullable
- `previous_value`, nullable
- `new_value`, nullable
- `changed_by`
- `change_reason`
- `approval_reference`, nullable
- `created_at`

## 3. State Model

Application status flow:

```text
Draft -> Pending
Pending -> Approved
Pending -> Rejected
Pending -> ManualReview
Pending -> Cancelled
Approved -> Expired
```

Loan status flow:

```text
Approved -> Disbursed -> Active -> Closed
Active -> Delinquent -> Active
Active -> Restructuring -> Active
Active -> ManualReview -> Active
Active -> ManualReview -> Closed
Delinquent -> ManualReview -> Delinquent
Delinquent -> WrittenOff
ManualReview -> WrittenOff
```

Rules:

- `Rejected`, `Cancelled`, `Expired`, `Closed` and `WrittenOff` are terminal.
- `Disbursed` means money has left the SACCO but repayment has not started.
- `Active` means the repayment schedule is running.
- `Delinquent` is calculated by the delinquency job, not manually set by admins.
- Admins may move a loan to `ManualReview`; they must not directly force normal business statuses.
- `Restructuring` blocks automated repayment processing and delinquency updates.
- `Closed` and `WrittenOff` are different outcomes and must not be merged in reports.

## 4. Eligibility and Collateral

Only active members may apply for loans.

Approval requires:

- member is active at approval time
- member has 6+ months of qualifying share contribution history
- repayment period is between 1 and 36 months
- at least 1 and at most 20 approved guarantors, unless this rule is explicitly changed
- applicant does not guarantee their own loan
- collateral rule passes
- minimum monthly principal is respected or explicitly approved as an exception

Eligibility must be checked at approval time and re-checked before disbursement.

Collateral rule:

```text
approved_amount <= min(
  3 * applicant_eligible_shares,
  applicant_eligible_shares
    + approved_guarantor_available_shares
    + eligible_deposits
)
```

Minimum monthly principal:

```text
minimum_monthly_principal = approved_principal / 36
```

Any lower monthly principal requires explicit approval and audit trail.

Share balances, contribution history, guarantor share availability and pledged share checks must come from the Shares domain using strongly consistent reads.

## 5. Disbursement

A loan may be disbursed exactly once.

Disbursement must run in one `ExecTx` and:

- lock the loan row with `FOR UPDATE`
- verify loan status is `Approved`
- re-check member eligibility through `member_service`
- re-check share and guarantor collateral through the Shares domain
- verify approved guarantor coverage
- write one disbursement transaction
- set loan status to `Disbursed`

The database must enforce at most one disbursement transaction per loan.

The disbursed amount must equal the approved principal unless a formally approved amendment exists before disbursement.

## 6. Repayment and Interest

Repayments may be accepted only for `Active`, `Disbursed` where repayment is allowed, or `Delinquent` loans. Repayments must be rejected for `Closed`, `WrittenOff`, `Restructuring` and terminal application states.

Loan interest for monthly contribution reporting is:

```text
interest_due = previous_loan_balance * 1%
```

Interest is zero when the previous loan balance is zero. Interest must not accrue after the loan is fully repaid.

Repayment allocation order:

```text
penalty -> interest -> principal -> credit
```

Principal application:

```text
principal_applied = min(payment_principal_component, outstanding_principal)
credit_created = payment_principal_component - principal_applied
new_outstanding_principal = outstanding_principal - principal_applied
```

Overpayment credit must be recorded in `CreditBalance`. Do not hide overpayments by clamping balances to zero.

Payment idempotency:

- external payments use `payment_gateway_transaction_id`
- internal/manual payments use `reference_id`
- duplicate attempts must return the original result
- retryable inserts must use `ON CONFLICT DO NOTHING` or an equivalent idempotent path

## 7. Payment Batching

Payments for a single loan may be batched within a configured time window to prevent penalty manipulation through many small payments.

Batch processing must:

- lock the loan row
- include all pending payments in the batch window
- apply allocation rules to the batch total
- write individual transaction rows with allocation breakdown
- update repayment schedule statuses
- complete idempotently

Open decision: the default batching window is currently 1 hour, but this must be confirmed before implementation.

## 8. Restructuring

Restructuring is the only normal path for changing repayment schedules after creation.

Restructuring must:

- lock the loan row
- move the loan to `Restructuring`
- block payment processing and delinquency updates
- supersede old schedule rows
- create new schedule rows
- preserve full before/after schedule audit
- require board approval for interest-rate changes
- return the loan to `Active` when complete

Term extensions must not exceed 36 months unless the business rule is changed.

## 9. Delinquency

Delinquency is calculated by a background job.

The job must:

- use `SELECT ... FOR UPDATE SKIP LOCKED`
- skip `Restructuring` loans
- skip `ManualReview` loans
- mark missed schedules
- move eligible loans to `Delinquent`
- move caught-up loans back to `Active`
- write audit entries for status changes

Humans can move a loan to `ManualReview`; humans cannot directly set `Delinquent`.

## 10. Write-Off and Closure

A loan closes only when principal, accrued interest and penalties are fully settled.

Write-off is allowed only when:

- remaining balance is zero, or
- the SACCO explicitly accepts the loss through documented approval

Write-off requires:

- loan row lock
- two-admin approval
- incident or board reference
- audit entry

Payments must be rejected after `Closed` or `WrittenOff`.

## 11. Guarantor Monitoring

For active loans, the system must monitor guarantor coverage.

If a guarantor becomes inactive, withdraws, loses sufficient available shares or exceeds guarantee limits, the loan must be flagged for `ManualReview`.

The monitoring process must not modify share balances directly. It reads from Members and Shares and updates only loan review state and audit records.

## 12. Cross-Domain Integration

Members:

- verify active status at application, approval and disbursement
- fail fast if `member_service` cannot verify eligibility
- store member ID and branch ID, not copied profile details

Shares:

- read current eligible shares with strong consistency
- read 6-month contribution history
- read guarantor available shares
- read pledged share commitments
- never write share balances from the Loans domain

Contribution and CEEP:

- repayment allocation may be initiated by a contribution receipt
- Loans remains authoritative for loan principal, interest, penalties and credit
- CEEP must snapshot Loans data, not calculate or mutate it

Events:

- loan status events must include `loan_id`, event type, previous status, new status, timestamp and per-loan sequence number
- consumers must not use events as the source of truth for critical decisions
- failed event processing must alert and retry

## 13. Database and Consistency Rules

- All multi-table loan writes use `ExecTx`.
- Loan-specific money operations lock the loan row with `FOR UPDATE`.
- Guarantor operations lock the guarantor commitment row.
- Do not lock whole tables.
- Use cursor pagination on `(created_at, id)`.
- Do not hard-delete rows.
- Financial transactions and audit records are append-only.
- Critical uniqueness must be enforced in the database.

Required database protections:

- unique disbursement transaction per loan
- unique external payment transaction ID per payment source
- unique manual repayment reference per loan
- valid repayment period: `repayment_period_months > 0 AND repayment_period_months <= 36`
- no negative transaction amounts unless the transaction type is a controlled reversal or adjustment
- no double reversal of the same transaction
- no reversal of a reversal

Do not implement cross-row business rules as `CHECK` constraints. Use transactions, locks, partial unique indexes and service logic instead.

## 14. Failure Modes

### Application and Approval

- Application accepted for inactive, suspended or withdrawn member.
- Application stores stale copied member identity.
- Approval uses stale share or member data.
- Requested loan exceeds collateral rule.
- Applicant guarantees their own loan.
- Guarantor is inactive or has insufficient available shares.
- Same collateral is pledged more than once.
- Loan is approved without required guarantor approvals.
- Approved amount differs from disbursed amount without approved amendment.

### Disbursement

- Loan is disbursed more than once.
- Rejected, cancelled or expired application is later disbursed.
- Member becomes ineligible between approval and disbursement.
- Share collateral drops below required amount before disbursement.
- Workflow crashes after transaction write but before status update.

### Repayment

- Duplicate gateway webhook applies repayment twice.
- Gateway sends different internal references for the same payment.
- Manual repayment is retried and applied twice.
- Payment is allocated to principal before penalties or interest.
- Loan balance is reduced by total payment instead of principal applied.
- Overpayment creates negative balance or untracked credit.
- Interest accrues after full repayment.
- Repayment is accepted for a closed, written-off or restructuring loan.
- Concurrent payments produce incorrect balance.

### Schedule and Delinquency

- First installment date is before disbursement.
- Last installment date does not match repayment period.
- Monthly principal is below `principal / 36` without approval.
- Missed installment does not create arrears or penalty.
- Delinquency is manually set by an admin.
- Delinquency job modifies a restructuring or manual-review loan.
- Restructuring silently overwrites old schedule rows.

### Guarantors and Collateral

- Same guarantor share value secures multiple loans beyond available value.
- Guarantor approval is missing or forged.
- Guarantor withdraws while securing an active loan.
- Guarantor becomes inactive and loan is not flagged.
- Share balance changes around approval/disbursement are not re-checked.

### Audit and Security

- Status, schedule, interest-rate or guarantor changes lack audit entries.
- Unauthorized user approves disbursement, restructuring, write-off or credit withdrawal.
- Manual adjustment changes financial state without two-admin approval.
- Records are updated or deleted instead of reversed, superseded or adjusted.
- Historical calculations change after rate or rule updates.

## 15. Resolved Inconsistencies

These were fixed while rewriting this spec:

- Split application state from loan state. The previous spec mixed `Pending`, `Rejected` and `Cancelled` with live loan statuses.
- Renamed the audit entity to `LoanAuditTrail`. The previous spec described audit fields under `CreditBalance`.
- Kept `CreditBalance` only for overpayments and credits.
- Removed duplicate invariant numbering.
- Replaced invalid cross-row `CHECK` constraint examples with enforceable database protections and service-level transactional rules.
- Made overpayment handling explicit instead of allowing balances to be silently clamped to zero.
- Made CEEP a consumer of loan data, not a calculator or mutator of loan balances.
- Clarified that Loans reads Shares and Members but does not write them.

## 16. Open Decisions

These must be settled before implementation:

- Is the default repayment batching window exactly 1 hour?
- Are zero-guarantor loans ever allowed for small amounts or special products?
- Does the 1% monthly interest apply to all active loans, or only specific loan products?
- Can repayment periods ever exceed 36 months after board-approved restructuring?
- What precise approval role can authorize monthly principal below `principal / 36`?
- Are overpayment credit withdrawals allowed at all, or should credit only offset future obligations?
