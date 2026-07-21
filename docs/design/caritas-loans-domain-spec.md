# Loans Domain Specification

_Note: This specification covers only loans and repayment tracking. It does not handle member management._

## 1. Important Definitions

- **Scope and Non-Goals:** What this system controls and what it specifically leaves to other systems.
- **Entities and Fields:** The core data objects. _Rule: Always use `decimal.Decimal` for money, never `float64`._
- **State Machine:** The valid statuses a loan can have, how it moves between them, and what triggers the change.
- **Invariants:** Strict rules that must always be true when data is saved, not just at the end of a process.
- **Failure Modes:** Potential error scenarios and the exact steps to recover from them.
- **Concurrency & Consistency:** Rules for locking data, adding new records, and preventing duplicate actions (idempotency).
- **External Triggers & Side Effects:** How this system interacts with scheduled background jobs (Temporal) and other domains.

## 2. Scope

- **What it owns:** Loan applications, approvals, disbursements (giving out the money), repayment schedules, and tracking repayments.
- **What it does NOT own:** Share balances (it reads this to check collateral) and Member statuses (it reads this to check eligibility).

## 3. Core Entities (Data Models)

### Loan

- `id`: UUID
- `member_id`: UUID
- `branch_id`: UUID
- `principal`: `decimal.Decimal` (The original loan amount)
- `interest_rate`: `decimal.Decimal` (Stored as a percentage rate, not a pre-calculated money amount)
- `repayment_period_months`: Integer (Maximum 36 months)
- `status`: Pending, Approved, Rejected, Disbursed, Restructuring, Active, Delinquent, Closed, Written Off, or Manual Review
- `disbursed_at`: Timestamp (Optional, filled when money is given out)
- `created_at`: Timestamp
- `updated_at`: Timestamp
- `updated_by`: UUID (Who last modified this record)
- `previous_status`: String (For audit trail of status changes)

### RepaymentSchedule

- `id`: UUID
- `loan_id`: UUID
- `installment_no`: Integer
- `due_date`: Date
- `amount_due`: `decimal.Decimal`
- `status`: Upcoming, Due, Paid, Missed, or Partial

### LoanTransaction

_An append-only ledger (records are added, never updated or deleted)._

- `id`: UUID
- `loan_id`: UUID
- `type`: Disbursement, Repayment, Penalty, Reversal, or Credit Withdrawal
- `amount`: `decimal.Decimal`
- `reference_id`: UUID (Used to prevent duplicate transactions)
- `payment_gateway_transaction_id`: String (External payment gateway's unique transaction ID for idempotency)
- `allocation_breakdown`: JSON (Details of how payment was allocated: principal, interest, penalty, credit)
- `created_at`: Timestamp
- `created_by`: UUID (Who initiated this transaction)

### LoanGuarantor

- `loan_id`: UUID
- `guarantor_id`: UUID (Member ID who guarantees the loan)
- `guaranteed_amount`: `decimal.Decimal` (Amount this guarantor is responsible for)
- `status`: Pending, Approved, or Rejected
- `approved_at`: Timestamp (Optional, filled when guarantor approval is confirmed)
- `approved_by`: UUID (Who approved this guarantor)
- `created_at`: Timestamp

_Separate tracking system for overpayments with strict withdrawal controls._

- `id`: UUID
- `member_id`: UUID
- `loan_id`: UUID (Nullable. Credit can be general or loan-specific)
- `amount`: `decimal.Decimal` (Current credit balance)
- `source`: String (How this credit was accumulated: "overpayment", "refund", "adjustment")
- `status`: Available, Frozen, or Withdrawn
- `created_at`: Timestamp
- `last_activity_at`: Timestamp

### CreditBalance

_Comprehensive audit logging for all critical loan changes._

- `id`: UUID
- `loan_id`: UUID
- `field_changed`: String (Which field was modified: "status", "interest_rate", "schedule")
- `previous_value`: String (Before the change)
- `new_value`: String (After the change)
- `changed_by`: UUID (Who made the change)
- `change_reason`: String (Why the change was made)
- `approval_reference`: String (Reference to approval document if required)
- `created_at`: Timestamp

## 4. State Machine: Loan Status

**The Flow:**

- `Pending` ➔ `Approved` (via `approve()`)
- `Pending` ➔ `Rejected` (via `reject()`)
- `Pending` ➔ `Manual Review` (If automatic approval fails and requires human intervention)
- `Approved` ➔ `Rejected` (If it times out or is withdrawn)
- `Approved` ➔ `Manual Review` (If collateral check fails at disbursement time)
- `Approved` ➔ `Disbursed` (via `disburse()` - only after successful guarantor verification and eligibility checks)
- `Disbursed` ➔ `Active` (Repayment period begins)

**Ongoing Management:**

- `Active` ➔ `Restructuring` (Via official restructure request - requires board approval for rate changes)
- `Restructuring` ➔ `Active` (When restructure completes and new schedule is active)
- `Active` ➔ `Delinquent` (Automatically calculated by background job checking for missed payments)
- `Delinquent` ➔ `Active` (When borrower catches up on payments)
- `Active` ➔ `Manual Review` (If unusual activity detected or manual intervention requested)
- `Delinquent` ➔ `Manual Review` (If collection strategies need human oversight)
- `Active` ➔ `Closed` (When fully repaid)
- `Delinquent` ➔ `Written Off` (If business decides to take the loss - requires balance = 0 check)
- `Manual Review` ➔ `Active` (When review complete and loan returns to normal status)
- `Manual Review` ➔ `Delinquent` (When review confirms delinquency)
- `Manual Review` ➔ `Closed` (When review leads to closure)
- `Manual Review` ➔ `Written Off` (When review leads to write-off)

**Important Rules:**

- **Disbursed vs. Active:** These are two different statuses. "Disbursed" means the money has left the business, but the repayment clock hasn't started yet (for example, during a grace period). This is an intentional business feature.
- **Closed vs. Written Off:** Both mean the loan has ended, but they mean completely different things (Success vs. Loss). They must never be mixed up in reports.
- **Restructuring is a Protected State:** When a loan is in `Restructuring` status, no automated processes (delinquency checks, payment processing) can modify it. This prevents race conditions during schedule rewrites.
- **Manual Review is Only Path for Human Intervention:** Admins cannot directly set any status except `Manual Review`. All other status changes must go through proper automated or approved processes.
- **Delinquent is Calculated, Never Manual:** The `Delinquent` status can only be set by the automated delinquency calculation job. Humans can only flag for `Manual Review`.
- **Interest Rate Changes Require Board Approval:** Any reduction in interest rate requires documented board-level approval and compensating term extension.

## 5. Invariants (Strict System Rules)

- **I1 — Approval Eligibility Verification:** A member can only receive a loan if:
  1. They have been consistently contributing shares for 6+ months
  2. Loan amount does not exceed 3x their total share balance
  3. They have sufficient guarantors (minimum 1, maximum 20)
  4. Minimum amount payable per month is Principal / 36 months
  5. All eligibility checks must happen at approval time and be re-verified before disbursement.

- **I2 — Exact-Once Disbursement with Row Locking:** A loan can only be disbursed once. The disbursement operation must:
  1. Start with `FOR UPDATE` lock on the loan row
  2. Verify loan status is `Approved` (not `Disbursed`)
  3. Re-verify eligibility: 6-month contribution consistency, 3x shares limit, and guarantor approvals
  4. Write disbursement transaction
  5. Update loan status to `Disbursed`
     All steps must happen in a single database transaction. Database-level unique constraint on `(loan_id, type)` where type = 'Disbursement' provides final protection.

- **I3 — Protected Schedule Restructuring:** Once a repayment schedule is created, it cannot be silently changed. If a schedule needs to change:
  1. Loan status must change to `Restructuring` first
  2. Restructuring process must lock the loan row for entire duration
  3. Delinquency job must skip loans with `Restructuring` status
  4. Full audit trail must record old and new schedules
  5. Interest rate changes require board-level approval

- **I4 — Controlled Overpayment Handling:** The total amount of repayments can never be higher than the original principal plus earned interest. Overpayments must:
  1. Be tracked in separate `CreditBalance` table
  2. Require same approval process as new loans for withdrawal
  3. Never auto-convert to withdrawable funds
  4. Be frozen if fraud suspected or account under investigation

- **I5 — Calculated Delinquency Only:** The `Delinquent` status is automatically calculated by a background job checking for `Missed` repayment schedules. Critical rules:
  1. Delinquent status can only be set by automated calculation
  2. Humans can only flag loans for `Manual Review`
  3. Delinquency job must skip `Restructuring` and `Manual Review` status
  4. Use `SELECT ... FOR UPDATE SKIP LOCKED` to avoid conflicts with active payments

- **I6 — Strong Idempotency with Gateway IDs:** A single payment must only apply once, even if payment gateway sends duplicate requests:
  1. Unique constraint on `(loan_id, payment_gateway_transaction_id)` - not reference_id
  2. Use payment gateway's internal transaction ID as primary idempotency key
  3. reference_id used only for internal tracking
  4. Duplicate attempts return original transaction result

- **I7 — Write-off Balance Validation:** A loan can only be written off if the remaining balance is exactly 0 or if the business explicitly accepts the loss:
  1. Write-off process must check balance within locked transaction
  2. If balance > 0, write-off rejected unless explicitly approved
  3. Repayment process must reject payments on `Written Off` or `Closed` loans
  4. Write-off requires two-admin approval and incident report

- **I8 — Batched Payment Processing:** All payments for a single loan within a configurable time window (default 1 hour) must be batched and processed together:
  1. Individual payments held in pending state
  2. Allocation rules applied to total batch amount
  3. Prevents manipulation of penalty calculation through multiple small payments
  4. Batch processing happens under single loan row lock

- **I7 — Guarantor Limits and Verification:** A loan can have 1-20 guarantors. Total guaranteed amounts from all approved guarantors must cover at least the loan principal. Guarantor verification must:
  1. Check guarantor is an active member with sufficient share balance
  2. Verify guarantor has not exceeded their guarantee limit (configurable, e.g., 5x their shares)
  3. Lock guarantor record during verification
  4. Track guarantor approval status and who approved it

- **I8 — Repayment Period Validation:** Loan repayment period cannot exceed 36 months. This must be enforced:
  1. At application submission (reject if > 36 months)
  2. During approval process
  3. During restructuring (term extensions cannot exceed original 36-month limit)
  4. Database constraint: `CHECK (repayment_period_months > 0 AND repayment_period_months <= 36)`

- **I9 — Share Contribution Consistency Verification:** Member must have 6+ months of consistent share contributions. Verification must:
  1. Check member's share transaction history for 6-month period
  2. Verify no gaps longer than 1 month in contribution history
  3. Calculate contribution consistency score (e.g., percentage of months with contributions)
  4. Reject if consistency score below threshold (e.g., 80%)
  5. Cache result for disbursement re-verification

- **I10 — Comprehensive Audit Trail:** All critical changes must be logged in `LoanAuditTrail`:
  1. Status changes: record previous_status, new_status, changed_by, reason
  2. Interest rate changes: record previous_rate, new_rate, approval_reference
  3. Schedule modifications: record full before/after schedule snapshot
  4. Guarantor changes: record guarantor_id, previous_status, new_status, approval_reference
  5. Admin interventions: always require documented business reason
  6. Audit trail is append-only, never updated or deleted

- **I11 — Database-Level Constraint Enforcement:** Critical invariants enforced at database level:

  ```sql
  -- Prevent negative balances
  ALTER TABLE loan_transactions
  ADD CONSTRAINT chk_positive_amounts
  CHECK (amount > 0);

  -- Prevent repayment period exceeding 36 months
  ALTER TABLE loans
  ADD CONSTRAINT chk_repayment_period_valid
  CHECK (repayment_period_months > 0 AND repayment_period_months <= 36);

  -- Prevent loans without sufficient guarantors
  ALTER TABLE loan_guarantors
  ADD CONSTRAINT chk_guarantor_count_valid
  CHECK (
     (SELECT COUNT(*) FROM loan_guarantors WHERE loan_id = loan_guarantors.loan_id AND status = 'approved') >= 1
  );

  -- Prevent status changes to Delinquent outside automated process
  ALTER TABLE loans
  ADD CONSTRAINT chk_delinquent_automated_only
  CHECK (
    status != 'Delinquent' OR
    updated_by = 'system_delinquency_job'
  );
  ```

- **I12 — Event Ordering and Processing:** All events must include sequence numbers per loan:
  1. Events emitted in strict order per loan
  2. Consumers must acknowledge receipt and processing
  3. Failed event processing triggers alerts and retry logic
  4. Never rely on events for critical business logic - database is source of truth

## 6. Failure Modes & Recovery

| Scenario                                                              | Required Behavior                                                                                                                                                                                                                                                                                                   |
| :-------------------------------------------------------------------- | :------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **Approval given, but member becomes ineligible before disbursement** | Re-verify eligibility: 6-month contribution consistency, 3x shares limit, and guarantor approvals (Rule I1). If fails, block disbursement and move loan to "Manual Review" status for human investigation.                                                                                                          |
| **Workflow crashes between saving disbursement and updating status**  | Both actions must happen inside single database transaction with loan row locked. If separate, second action must be safely retryable with idempotency checks.                                                                                                                                                      |
| **Payment gateway sends same repayment twice**                        | Rely on database unique constraint (`loan_id`, `payment_gateway_transaction_id`). Second attempt ignored and returns first result.                                                                                                                                                                                  |
| **Repayment doesn't perfectly match due amount (Partial Payment)**    | Code must follow strict business rule for allocation (Pay Penalty first, then Interest, then Principal). Multiple payments within 1 hour batched together (Rule I8).                                                                                                                                                |
| **Write-off happens at same time as repayment arrives**               | Lock loan row during status change. Write-off must verify balance = 0 within locked transaction (Rule I7). Repayment checks loan status before applying.                                                                                                                                                            |
| **Delinquency job runs while loan being restructured**                | Job must check `status != Restructuring` and `status != Manual Review`. If restructuring, skip and check next time (Rule I3, I5).                                                                                                                                                                                   |
| **Member attempts to manipulate contribution history**                | Use full 6-month contribution history verification, not just current balance (Rule I9). Track contribution patterns and flag irregular patterns for manual review.                                                                                                                                                  |
| **Attacker overpays to manipulate credit system**                     | Overpayments tracked in separate CreditBalance table with withdrawal controls (Rule I4). Credit withdrawals require same approval as new loans.                                                                                                                                                                     |
| **Payment gateway sends different reference IDs for same payment**    | Use `payment_gateway_transaction_id` for idempotency, not generated reference_id (Rule I6). Unique constraint on gateway ID prevents duplicate processing.                                                                                                                                                          |
| **Insufficient guarantor coverage before disbursement**               | Verify total guaranteed amount from approved guarantors >= loan principal (Rule I7). If insufficient, block disbursement and move to "Manual Review" for additional guarantors or adjustment.                                                                                                                       |
| **Data corruption or invariant violation detected**                   | **Recovery Procedure:**<br>1. Lock affected loans from all operations<br>2. Create detailed incident report<br>3. Manual investigation by two separate admins<br>4. Recovery transactions approved by both admins<br>5. Re-run validation to verify fix<br>6. Document lessons learned<br>7. Monitor for recurrence |

## 7. Concurrency & Consistency

- **Disbursements:** Must follow strict sequence:
  1. Lock loan row with `FOR UPDATE`
  2. Verify loan status = `Approved`
  3. Re-verify eligibility: 6-month contribution consistency, 3x shares limit, and guarantor approvals
  4. Write disbursement transaction
  5. Update loan status to `Disbursed`
  6. Release lock
     All steps in single database transaction.

- **Repayments:** Must follow strict sequence:
  1. Lock loan row with `FOR UPDATE`
  2. Check loan status allows payments (not `Written Off`, `Closed`, `Restructuring`)
  3. Check for pending payments in batching window
  4. If window active, add to pending batch
  5. If window expired, process entire batch together
  6. Apply allocation rules to batch total
  7. Write individual transaction records with allocation breakdown
  8. Update repayment schedule statuses
  9. Release lock

- **Delinquency Checks:** Background job uses `SELECT ... FOR UPDATE SKIP LOCKED` to:
  1. Skip loans currently being paid (locked)
  2. Skip loans in `Restructuring` status
  3. Skip loans in `Manual Review` status
  4. Process next batch of available loans
  5. Set `Delinquent` status only via automated calculation

- **Restructuring Process:** Must follow strict sequence:
  1. Lock loan row with `FOR UPDATE`
  2. Change status to `Restructuring`
  3. Create audit trail entry for restructure initiation
  4. Lock repayment schedule records
  5. Write new schedule records
  6. Update old schedule records as superseded
  7. Create audit trail with full before/after snapshot
  8. If interest rate changed, require board approval reference
  9. Change status to `Active` with new schedule
  10. Release all locks

- **Credit Withdrawals:** Must follow same approval process as new loans:
  1. Verify credit balance exists and is `Available`
  2. Require two-admin approval
  3. Create audit trail entry
  4. Process withdrawal with same controls as disbursement
  5. Update credit status to `Withdrawn` or reduced

- **Lock Granularity:** Always lock at the loan row level for loan-specific operations. For guarantor operations, lock individual guarantor records. Never lock entire tables.

## 8. External Triggers

- **Shares Domain:** The loan system reads share balances and contribution history to check eligibility (Rule I1, I9). This is read-only with critical security requirements:
  - Must call shares domain API with `consistency=strong` flag for primary DB read
  - Must retrieve full 6-month contribution history for consistency verification
  - Must verify current share balance for 3x shares limit enforcement
  - Must verify guarantor share balances for guarantee capacity

- **Guarantor Monitoring (Continuous):** Background process monitors all active loans:
  - Check guarantor status changes (member becomes inactive, shares drop below threshold)
  - If guarantor becomes ineligible, alert for manual review and additional guarantors
  - If total guaranteed coverage drops below principal, trigger margin call or require additional guarantors

- **Event Notifications (Redpanda):** Emits messages when loan status changes for other systems:
  - Events must include `sequence_number` per loan for ordering
  - Events must include `event_type`, `loan_id`, `previous_status`, `new_status`, `timestamp`
  - Consumers must acknowledge receipt and processing success
  - Failed event processing triggers alerts and retry logic
  - Never rely on events for critical business logic - database is source of truth
  - Event types: Disbursed, Active, Delinquent, Restructuring, Closed, Written Off, Manual Review

- **Temporal Workflows:** Manages complex multi-step processes:
  - Disbursement process with eligibility verification and guarantor checks
  - Payment batching within time windows
  - Long-term monitoring loops (using `ContinueAsNew`) for repayment schedules
  - Guarantor coverage monitoring and eligibility alerts
  - Restructuring workflow with approval gates and audit trails

- **Payment Gateway Integration:** Handles external payment processing:
  - Must use gateway's internal transaction ID for idempotency
  - Must handle webhook timeouts with retry logic
  - Must validate webhook signatures to prevent spoofing
  - Must store raw webhook payload for audit purposes

## 9. Security Monitoring & Alerting

The system must trigger real-time alerts for suspicious patterns indicating potential attacks or system abuse:

- **Multiple Failed Disbursements:** Same loan or member has 3+ failed disbursement attempts in 24 hours (indicates eligibility manipulation attempts)

- **Rapid Share Balance Changes:** Member's share balance changes multiple times within short window around loan approval (indicates timing attacks on 3x shares limit)

- **Irregular Contribution Patterns:** Member has gaps or irregular patterns in 6-month contribution history that suggest manipulation (indicates contribution history attacks)

- **Unusual Payment Patterns:** Multiple small partial payments from same source within batching window (indicates penalty avoidance attempts)

- **Credit Manipulation:** Rapid overpayment followed by immediate credit withdrawal request (indicates credit system exploitation)

- **Restructuring Requests:** Multiple restructure requests for same loan within short period (indicates interest rate manipulation attempts)

- **Gateway ID Collisions:** Payment gateway sends duplicate transaction IDs (indicates gateway issues or potential spoofing)

- **Status Violations:** Any attempt to set `Delinquent` status outside automated process (indicates manual override attempts)

- **Constraint Violations:** Database constraint failures indicate attempted exploitation (should trigger immediate security review)

- **Audit Trail Gaps:** Missing audit entries for critical changes (indicates system bypass attempts)

- **Failed Admin Actions:** Multiple failed admin login attempts or unauthorized access attempts (indicates credential attacks)

- **Guarantor Manipulation:** Same member appears as guarantor for excessive number of loans or attempts to guarantee beyond capacity (indicates guarantee system exploitation)

- **Repayment Period Violations:** Any attempt to set repayment period > 36 months (indicates business rule bypass attempts)

All security alerts must:

1. Create incident record in security monitoring system
2. Notify security team immediately
3. Freeze affected accounts/loans if attack confirmed
4. Require manual security review before unfreezing
5. Create detailed audit trail of security incident and response
