# Shares Domain Specification

This spec owns share accounts, share purchases, share withdrawals, share balances, pledged shares, guarantor share commitments, share adjustments and dividend posting to the share ledger.

It does not own member identity, member lifecycle, loan approval, loan repayment, loan collateral valuation, contribution receipt orchestration or CEEP reporting.

## 1. Domain Boundaries

- Member identity, branch assignment and member status are owned by `member_service`.
- Loan applications, loan approvals, disbursements, repayments, interest and penalties are owned by the Loans domain.
- Contribution receipts and CEEP reporting are coordinated by the cross-domain contribution spec.
- The Shares domain is authoritative for share balances and available share security.
- Other domains may read share balances and pledge availability with strong consistency. They must not write share accounts or share ledger rows directly.

## 2. Core Records

Use decimal money types only. Do not use floats.

### ShareAccount

- `id`
- `member_id`
- `branch_id`
- `status`: Active, Dormant, Frozen, Closed
- `opened_at`
- `closed_at`, nullable
- `created_at`
- `updated_at`

### ShareTransaction

Share transactions are append-only. They are never updated or deleted.

- `id`
- `share_account_id`
- `type`: Purchase, Withdrawal, Dividend, Reversal, Adjustment
- `amount`
- `balance_after`
- `reference_id`
- `reversal_of`, nullable
- `reason`
- `originator_id`
- `created_at`

### SharePledge

Pledges reserve shares as security for loans or guarantor commitments.

- `id`
- `share_account_id`
- `loan_id`
- `pledged_amount`
- `type`: ApplicantSecurity, GuarantorSecurity
- `status`: Pending, Active, Released, Cancelled
- `approved_by`, nullable
- `approved_at`, nullable
- `released_at`, nullable
- `created_at`

### ShareAdjustment

Manual corrections require strict approval and audit.

- `id`
- `share_transaction_id`
- `reason`
- `audit_report_id`
- `approved_by`
- `approved_at`
- `created_at`

### DividendAllocation

The Shares domain owns dividend posting to share accounts. The cross-domain spec defines yearly dividend calculation inputs.

- `id`
- `member_id`
- `share_account_id`
- `financial_year`
- `eligible_share_balance`
- `rate`
- `dividend_amount`
- `status`: Draft, Approved, Posted, Reversed
- `approved_by`
- `posted_transaction_id`, nullable
- `created_at`
- `posted_at`, nullable

## 3. Account State Model

```text
New -> Active
Active -> Dormant
Dormant -> Active
Active -> Frozen
Dormant -> Frozen
Frozen -> Active
Frozen -> Dormant
Active -> Closed
Dormant -> Closed
```

Rules:

- `Closed` is terminal. A closed account must not reopen.
- Closure requires zero share balance and zero active pledged shares.
- `Dormant` accounts cannot withdraw, pledge or receive dividend postings until reactivated.
- `Frozen` accounts cannot transact except through approved reversal, adjustment or release workflows.
- Reactivation from `Dormant` requires approval and balance verification.
- Unfreezing requires the incident or audit process that caused the freeze to be resolved.

## 4. Balance Rules

The latest share balance is the latest `balance_after` on the share ledger.

```text
new_balance = previous_balance + purchase_amount
new_balance = previous_balance + dividend_amount
new_balance = previous_balance - withdrawal_amount
```

Balances must never be negative.

Available shares for withdrawal or pledge:

```text
available_shares = current_share_balance - active_pledged_amount
```

A member cannot withdraw or pledge more than `available_shares`.

Balance-changing operations must run in one `ExecTx` and:

- lock the share account row with `FOR UPDATE`
- read the latest ledger balance
- verify account status allows the operation
- verify the resulting balance is non-negative
- write one append-only transaction
- persist `balance_after`

Do not recalculate operational balances from scratch during writes. Historical recalculation is for audit only.

## 5. Purchase and Contribution Integration

Share purchases may be initiated by a contribution receipt, manual office entry or another approved source.

Every retryable share purchase must use an idempotency reference. For contribution-originated purchases, use the contribution receipt allocation reference.

The Shares domain records the share purchase. It does not decide how a full contribution is split between shares, fees, loan principal, loan interest or penalties.

CEEP snapshots must read finalized Shares ledger data. CEEP must not write shares.

## 6. Withdrawals

Share withdrawal must:

- lock the share account
- verify account status allows withdrawal
- verify requested amount is greater than zero
- verify requested amount does not exceed available shares
- write a Withdrawal transaction
- keep `balance_after` non-negative

Membership withdrawal is coordinated by the cross-domain spec. The Shares domain only settles the eligible share balance and closes the share account when all loan and pledge checks have passed.

## 7. Pledges and Guarantor Security

The Shares domain owns reservation of shares used as loan security.

A pledge may become `Active` only when:

- the share account is Active
- the member is eligible according to `member_service`
- pledged amount is greater than zero
- pledged amount does not exceed available shares
- the related loan reference is valid
- required approval exists

The same share value must not secure multiple active obligations beyond the member's available balance.

Loans may request pledge creation, release or availability checks through the Shares API. Loans must not update pledge rows directly.

Guarantor approval belongs to the Loans workflow, but share reservation belongs to the Shares domain.

## 8. Dividends

Dividend calculation is defined by the cross-domain spec. Dividend posting to the share ledger is owned by the Shares domain.

Posting dividends must:

- use one approved allocation per member and financial year
- be idempotent
- write a Dividend share transaction
- store the posted transaction ID on the allocation
- prevent more than one posted dividend allocation per member and financial year

Corrections must create an adjustment or reversal. Do not overwrite a posted dividend transaction.

## 9. Reversals and Adjustments

Mistakes in the ledger are corrected by append-only records.

Reversal rules:

- a reversal points to the original transaction through `reversal_of`
- the reversal amount is derived from the original transaction, not manually entered
- one transaction may be reversed at most once
- a reversal cannot reverse another reversal
- reversals require a new `reference_id`

Adjustment rules:

- adjustments are separate from reversals
- adjustments require documented reason
- adjustments require approval
- adjustments must link to an audit report or incident reference
- adjustments must write a ShareTransaction of type `Adjustment`

## 10. Cross-Domain Reads

Loans may request:

- current share balance
- available shares
- active pledged amount
- 6-month contribution history
- guarantor pledge availability

Decision reads must be strongly consistent and must not come from a stale replica.

Display reads may use a replica only when the result is not used for transactional decisions.

If `member_service` or the Shares service cannot verify required eligibility, the caller must fail fast and retry through the workflow. It must not proceed on assumed status or assumed balance.

## 11. Database and Consistency Rules

- All multi-table share writes use `ExecTx`.
- Money operations lock the share account row with `FOR UPDATE`.
- Pledge operations lock the share account row and relevant pledge rows.
- Do not lock whole tables.
- Use cursor pagination on `(created_at, id)`.
- Do not hard-delete rows.
- Share transactions and audit records are append-only.
- Retryable inserts use `ON CONFLICT DO NOTHING` or an equivalent idempotent path.

Required database protections:

- unique `(share_account_id, reference_id, type)` for idempotent transactions
- `balance_after >= 0`
- transaction `amount > 0` except controlled reversal or adjustment semantics
- one reversal per original transaction
- reversal must point to an existing non-reversal transaction
- no reversal of a reversal
- no more than one active share account per member, unless the business explicitly allows multiple accounts
- no more than one posted dividend allocation per member and financial year

Do not implement cross-row business rules as `CHECK` constraints. Use foreign keys, partial unique indexes, transactions, locks and service logic.

## 12. Failure Modes

### Account Lifecycle

- Share account is opened for inactive or wrong member.
- More than one active share account exists for the same member unexpectedly.
- Closed account is reopened.
- Account is closed with nonzero balance.
- Account is closed with active pledged shares.
- Dormant or frozen account is allowed to withdraw or pledge.
- Member closure proceeds before share account closure.

### Transactions

- Share purchase is applied twice after contribution retry.
- Withdrawal creates negative balance.
- Withdrawal ignores pledged shares and uses total balance instead of available balance.
- Dividend or adjustment posts to wrong share account.
- `balance_after` does not match previous balance plus transaction effect.
- Financial transaction is updated or deleted instead of reversed or adjusted.
- Backdated transaction changes a finalized reporting period without audit.

### Pledges and Guarantor Security

- Same shares secure multiple active loans beyond available amount.
- Guarantor shares are pledged without approval.
- Applicant or guarantor pledges more shares than owned.
- Pledge remains active after related loan is closed.
- Pledge is released while the loan obligation is still active.
- Share balance changes around loan approval or disbursement are not re-checked.

### Reversals and Adjustments

- Same transaction is reversed more than once.
- Reversal amount is manually entered and differs from original.
- Reversal points to another reversal.
- Adjustment is created without approval or audit reference.
- Failed operation is hidden by rollback without an audit-visible correction after external effects occurred.

### Dividends

- Dividend posts more than once for same member and financial year.
- Dividend posts from stale or unapproved allocation.
- Dividend posts to inactive, dormant, frozen or closed account.
- Correction overwrites original dividend transaction.
- Total share balance in reports differs from the Shares ledger after posting.

### System-Wide

- Concurrent updates overwrite one another.
- Stale replica read is used for loan collateral or withdrawal decision.
- Partial processing leaves contribution receipt, share ledger and CEEP inconsistent.
- Historical reports change after rate or rule updates.
- Imported share data has missing, malformed or duplicate rows.
- Currency precision or rounding differs from other financial domains.

## 13. Resolved Inconsistencies

These were fixed while rewriting this spec:

- Added `Frozen` to the account status model because the old recovery procedure used it without defining it.
- Added `SharePledge` because the cross-domain and Loans specs require pledged shares and guarantor share commitments.
- Clarified that Loans values collateral but Shares owns share reservation and available balance.
- Clarified that contribution orchestration and CEEP reporting do not belong to Shares.
- Replaced invalid cross-row `CHECK` constraint examples with enforceable database protections and service-level transactional rules.
- Removed the instruction to use database rollback as an audit mechanism. Rollbacks are correct before commit; reversals are required after an externally visible or committed financial effect must be corrected.
- Made dividend posting a Shares responsibility while leaving dividend calculation rules in the cross-domain spec.

## 14. Open Decisions

These must be settled before implementation:

- Can a member ever have more than one share account, or is one active share account per member mandatory?
- What exact inactivity period moves an account from `Active` to `Dormant`?
- Who can approve dormant reactivation and frozen-account unfreezing?
- Are share withdrawals allowed outside full membership withdrawal?
- Should dividend posting be blocked for `Dormant` accounts or only for `Frozen` and `Closed` accounts?
- What exact external reference identifies the related loan when creating or releasing a pledge?
