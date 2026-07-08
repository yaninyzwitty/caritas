# Shares Domain Specification

_Note: This specification covers only shares and savings, not member management._

## 1. Important Definitions

- **Scope and Non-Goals:** What this domain controls, and what it leaves to other domains.
- **Entities and Fields:** The core data objects and their properties. _Rule: Always use `decimal.Decimal` for money, never `float64`._
- **State Machine:** The valid statuses an account can have, how it moves between them, and what triggers the change.
- **Invariants:** Strict rules that must always be true when data is saved to the database.
- **Failure Modes:** Potential errors and the exact steps to recover from them.
- **Concurrency & Consistency:** Rules for locking data, adding new records, and preventing duplicate actions (idempotency).
- **External Triggers & Side Effects:** How this system interacts with scheduled jobs (Temporal) and other domains.

## 2. Scope

- **What it owns:** The lifecycle of a share account, buying/withdrawing shares, distributing dividends, and checking balances.
- **What it does NOT own:** Valuing loan collateral (the Loan domain can read our balances, but cannot change them) and member KYC status (owned by the Member domain).

## 3. Entities (Data Models)

### ShareAccount

- `id`: UUID
- `member_id`: UUID
- `branch_id`: UUID
- `status`: Active, Dormant, or Closed
- `opened_at`: Timestamp

### ShareTransaction

_This is an append-only ledger. Records are never updated or deleted._

- `id`: UUID
- `reason`: String (Description of the transaction)
- `originator_id`: UUID (Who initiated it)
- `share_account_id`: UUID
- `type`: Purchase, Withdrawal, Dividend, Reversal, or Adjustment
- `amount`: `decimal.Decimal`
- `balance_after`: `decimal.Decimal` (Calculated and saved at the exact time of the transaction)
- `reference_id`: UUID (Used to prevent duplicate transactions)
- `reversal_of`: UUID (Nullable. Points to the ID of the transaction being reversed)
- `created_at`: Timestamp

### ShareAdjustment

_Separate table for manual corrections with strict audit requirements._

- `id`: UUID
- `share_transaction_id`: UUID (Links to the Adjustment transaction)
- `approver_id`: UUID (The admin who approved this adjustment)
- `reason`: String (Detailed justification for the adjustment)
- `audit_report_id`: UUID (Reference to the external audit report triggering this fix)
- `created_at`: Timestamp

## 4. State Machine: Share Account

**Transitions:**

- **New Account** ➔ `Active` (via `open()`)
- **Active` ➔ `Dormant` (If there is no activity for N days)
- **Dormant` ➔ `Active` (via `reactivate()` — **requires manual approval and balance verification**)
- **Active` ➔ `Closed` (via `close()`, but **only if the balance is exactly 0**)

**Important Rule:** `Closed` is a final, permanent state. You cannot reopen a closed account. If a user needs an account again, they must open a new one. This prevents confusion over whether old transactions belong to the current active account.

**Dormant Account Security:** When an account becomes Dormant, its balance is effectively frozen. Reactivation triggers an audit to verify no forfeited amounts exist and requires two-factor approval from separate admin users.

## 5. Invariants (Strict System Rules)

- **I1 — No Negative Balances:** The `balance_after` must always be 0 or higher. Withdrawals that drop the balance below zero must be blocked _before_ they are written to the database. **Critical:** The balance check and transaction write must occur within a single atomic database transaction that locks the share_account row with `FOR UPDATE` to prevent race conditions.

- **I2 — Append-Only Ledger:** Never use `UPDATE` or `DELETE` on transactions. Mistakes are fixed by adding a new "Reversal" transaction.
  - **No Silent Rollbacks:** Never use database transaction rollbacks to undo failed operations. If a transaction fails, you must explicitly write a "Reversal" transaction to maintain the audit trail. Use ExecTx to ensure the reversal always accompanies the failure.
  - Reversals need a new `reference_id` (to prove it's a new action, not a retry) and a `reversal_of` field (pointing to the ID of the bad transaction).

- **I2a — No Double Reversals:** A transaction can only be reversed once. This must be enforced by a unique database constraint on the `reversal_of` column to prevent attackers from reversing a single fraudulent transaction multiple times to extract extra money.

- **I2b — Exact Reversal Amounts:** The system must automatically pull the exact negation (opposite amount) from the original transaction. No one should manually type in the reversal amount, to prevent typos and incorrect adjustments.

- **I2c — Do Not Reverse a Reversal:** You cannot point a reversal at another reversal. Enforce this at the database level. If a reversal was made by mistake, fixing it requires a special, highly restricted manual "Adjustment" process. This prevents malicious actors from hiding fraudulent loops (reversing a reversal to quietly restore stolen funds).

- **I3 — Balances Must Add Up:** The total math (Purchases + Dividends - Withdrawals + Reversals) must perfectly match the latest `balance_after`. This should be tested during regular system audits. **Calculation Rule:** Always calculate `balance_after` by reading the latest transaction's `balance_after` and adding/subtracting the new amount. Never recalculate from scratch to avoid precision loss.

- **I4 — Idempotency (No Duplicates):** Two transactions for the same account with the same `reference_id` and same `transaction_type` must never both succeed. Enforce this with a unique database constraint on `(share_account_id, reference_id, transaction_type)` to prevent collision across different operation types.

- **I5 — Zero Balance on Closure:** The system must check that the balance is 0 at the exact moment the account status changes to `Closed`. **Critical:** This check must happen after acquiring a `FOR UPDATE` lock on the share_account row to prevent concurrent operations (like dividend deposits) from changing the balance between check and write.

- **I6 — Manual Adjustments Require Approval:** Any `Adjustment` transaction (manual fix) requires:
  - Separate transaction_type enum: `Adjustment` not `Reversal`
  - Two-factor approval from separate admin users
  - Audit log with detailed reason and approver details
  - Entry in the `share_adjustments` table with audit report reference

- **I7 — Database-Level Enforcement:** Invariants must be enforced at the database level, not just application level:
  ```sql
  -- Prevent negative balances
  ALTER TABLE share_transactions
  ADD CONSTRAINT chk_non_negative_balance
  CHECK (balance_after >= 0);

  -- Prevent reversing non-existent or already-reversed transactions
  ALTER TABLE share_transactions
  ADD CONSTRAINT chk_valid_reversal
  CHECK (type != 'Reversal' OR reversal_of IN (
    SELECT id FROM share_transactions WHERE type != 'Reversal'
  ));

  -- Prevent reversing a reversal
  ALTER TABLE share_transactions
  ADD CONSTRAINT chk_no_reverse_reversal
  CHECK (type != 'Reversal' OR NOT EXISTS (
    SELECT 1 FROM share_transactions t2
    WHERE t2.id = share_transactions.reversal_of AND t2.type = 'Reversal'
  ));
  ```

## 6. Failure Modes & Recovery

- **Interrupted Jobs:** If a background job stops and restarts, use the `reference_id` to skip accounts that were already processed. Do not just rely on where the job "thinks" it left off.

- **Audit Failures (Rule I3 fails):** If an account's math doesn't add up during a scheduled check, trigger alerts and freeze the account (change its status or add a flag). **Do not attempt to auto-correct the ledger.** It requires manual investigation.
  - **Recovery Procedure:**
    1. Lock account from all operations (set status to `Frozen`)
    2. Identify the discrepancy start point by comparing ledger history
    3. Manual investigation creates detailed audit report
    4. If fix is required, create `Adjustment` transaction with reference to audit report ID
    5. Entry must be approved by two separate admin users
    6. Re-run audit to verify discrepancy is resolved
    7. Unfreeze account only after verification passes

- **Concurrent Operation Conflicts:** If two operations attempt to modify the same account simultaneously, the `FOR UPDATE` lock will serialize them. The second operation will wait for the first to complete, then see the updated balance and proceed (or fail if insufficient funds). No special recovery needed—this is expected behavior.

- **Database Constraint Violations:** If an operation violates I7 database constraints, the transaction fails immediately. Application code should catch this and return a clear error message indicating which invariant was violated. This indicates either a bug in the application logic or attempted malicious activity.

## 7. Concurrency & Consistency

- **Writing Data (Money Math):** When changing an account's balance, lock the specific database row (`FOR UPDATE`). Never use optimistic retries for money calculations, as this can easily cause incorrect totals. **Critical:** The balance check and transaction write must occur within the same locked transaction to prevent race conditions (see I1 and I5).

- **Reading Data:**
  - **Display Reads:** General display reads can come from a delayed database replica for performance.
  - **Decision Reads:** If a read is being used to make a new transactional decision (like approving a loan based on shares), it must read from the primary, real-time database with strong consistency.
  - **External System Reads:** Other domains must call the shares domain API with a `consistency=strong` flag. They must not query the replica directly, as stale data could lead to incorrect decisions (e.g., approving a loan based on outdated collateral value).

- **Lock Granularity:** Always lock at the share_account row level. Never lock entire tables, as this kills performance under load. The row lock is sufficient to prevent concurrent modifications to the same account.

## 8. External Triggers

- **Loan System:** Reads the latest `balance_after` to calculate collateral. This is strictly read-only; loans cannot write to shares.
  - **Consistency Requirement:** Loan domain must call shares domain API with `consistency=strong` flag to force primary DB read. Never query the replica directly, as stale balance data could lead to incorrect loan approvals and collateral coverage mismatches.

- **Member System:** When a member tries to close their profile, their system triggers this domain to verify the account is properly closed (Rule I5) before allowing the member closure to finish.

- **Real-Time Monitoring (Security):** The system must trigger alerts for suspicious patterns:
  - Multiple reversals for the same user within a short time window
  - Large withdrawals immediately after dormant account reactivation
  - Rapid balance fluctuations (potential double-spend attempts)
  - Brute force patterns in reference_id generation
  - Failed constraint violations indicating attempted exploitation
