# Caritas Cross-Domain Specification

This spec coordinates member, share, loan, contribution, CEEP, withdrawal and dividend behavior. It DOES NOT own member, share or loan state.

## 1. Domain Ownership

- Members are owned by `member_service`.
- Member identity, lifecycle, status, branch and profile data must not be copied into other domains for convenience.
- Shares, share purchases, share withdrawals, pledges, guarantor share commitments, dividends and share balances are owned by the Shares ledger.
- Loan applications, approvals, disbursements, repayments, interest, penalties, overpayments and loan balances are owned by the Loans ledger.
- CEEP is a reporting snapshot only. It must never create, update or correct member, share or loan balances.

Cross-domain writes must go through the owning service. Reports may join or snapshot data, but reports are not authoritative state.

## 2. Authoritative Records

### ContributionReceipt

Every received contribution must create exactly one `ContributionReceipt`, whether the source is cash, Lipa na M-Pesa, Paybill or another approved channel.

The receipt must contain:

- source channel
- external transaction ID, where available
- member ID
- branch ID
- contribution period
- received amount
- immutable allocation plan
- processing status
- created by or received by
- received at

Daraja webhook processing must be idempotent. The gateway transaction ID is the preferred idempotency key. Manual cash entries need a unique internal receipt reference.

### AllocationPlan

The allocation plan states how the received amount is applied to:

- COM
- LGOM
- share purchase
- loan principal
- loan interest
- penalties
- other approved charges
- overpayment credit, if any

A receipt is completed only after every required authoritative allocation has succeeded in its owning domain. Failed or partial receipts must not appear as completed in CEEP.

### CEEP Snapshot

One CEEP snapshot must exist per active member per contribution period.

The CEEP snapshot is immutable after finalization and must be derived from completed authoritative records only. It must use a declared reporting cutoff time. Pending, failed or partially processed receipts are excluded and listed for reconciliation.

Recommended CEEP fields:

- record number
- member number
- member display name
- COM
- LGOM
- previous shares
- shares paid
- total shares
- sale of literature
- Caritas registration
- group registration
- LSF
- principal disbursed
- monthly loan principal paid
- previous loan balance
- loan interest
- loan balance after payment
- laptop
- penalty
- total amount paid

## 3. Contribution Rules

COM and LGOM are monthly membership charges assessed once per active member per applicable contribution period.

- COM amount: 30
- LGOM amount: 30
- Mandatory unless a valid approved exemption exists for the member, fee type and period.
- Must not be assessed for periods after the member withdrawal effective period.
- Treatment of the withdrawal month depends on the configured monthly assessment date.

Every exemption must identify:

- member
- fee type
- reason
- effective start and end period
- approving officer
- approval date
- status

Fee rates must be effective-dated and non-overlapping. Historical receipts and finalized CEEP snapshots must keep the fee-rate version used at the time.

Loan interest is not a membership fee. It is calculated by the Loans domain as 1% of the previous loan balance only when the previous balance is greater than zero and the loan was active during the contribution period.

## 4. Formulas

Use decimal money types only. Do not use floats.

```text
total_monthly_paid =
  loan_principal_paid
  + loan_interest_paid
  + shares_paid
  + COM
  + LGOM
  + penalties
  + other_approved_charges
```

```text
loan_interest_due = previous_loan_balance * 1%
```

Interest is zero when `previous_loan_balance` is zero.

```text
principal_applied = min(payment_principal_component, outstanding_principal)
credit_created = payment_principal_component - principal_applied
new_loan_balance = outstanding_principal - principal_applied
```

Overpayments must be tracked by the Loans domain as credit. Do not hide overpayments by clamping the balance to zero.

```text
total_shares = previous_shares + shares_paid
```

The Shares domain is authoritative for the final share balance.

## 5. Loan Application Rules

Only active members may apply for loans.

Loan applications must store:

- member ID
- branch ID
- declared loan-specific fields
- requested amount
- loan purpose
- monthly income, if required for lending review
- references to member-service verification results

Loan applications must not copy name, national ID, phone number or profile details into Loans except as legally required immutable audit snapshots.

Collateral and eligibility must be checked at approval time and re-checked before disbursement using strongly consistent reads.

```text
approved_amount <= min(
  3 * applicant_eligible_shares,
  applicant_eligible_shares
    + approved_guarantor_available_shares
    + eligible_deposits
)
```

The first installment due date must be after the disbursement date. The last installment due date must match the approved repayment period. The repayment period must not exceed 36 months unless the Loans spec is changed to allow it.

Minimum monthly principal:

```text
minimum_monthly_principal = principal / 36
```

Any exception requires explicit approval and an audit trail.

## 6. Membership Withdrawal Rules

Every withdrawal must use one unique idempotent withdrawal request.

Starting withdrawal moves the member to `withdrawal_pending`. While pending, the member cannot receive new loans, loan disbursements, guarantor commitments, share pledges or other new obligations.

A withdrawal may proceed only when authoritative checks confirm:

- no outstanding loan obligations
- no pending loan disbursements
- no pledged shares
- no active guarantor obligations
- no unsettled contribution receipts

The Shares domain must settle eligible shares and close the share account before `member_service` closes the member.

```text
net_withdrawal_amount =
  shares_at_withdrawal
  - unpaid_group_dues
  - other_approved_deductions
```

The result must not be negative. Retrying a withdrawal step must not repeat a deduction, payment, share withdrawal or member closure.

Withdrawal records must include:

- member ID
- member number or display reference
- reason
- shares at withdrawal
- unpaid group dues
- other approved deductions
- prepared by
- checked by
- approved by
- effective period
- status

If separation of duties is required, the same person must not prepare, check and approve the same request.

## 7. Dividend Rules

Annual dividend allocation uses the approved dividend rate for the applicable financial year. The default rate is 5% unless another effective-dated rate has been formally approved.

The calculation base is the member's average finalized month-end eligible share balance during the financial year.

```text
eligible_share_balance =
  sum(eligible finalized month-end share balances) / eligible_month_count

dividend_amount =
  eligible_share_balance * annual_dividend_rate
```

Dividend calculations must use finalized historical balances from the Shares ledger. Current share balances and CEEP-calculated balances must not be substituted.

Every allocation must record:

- member ID
- financial year
- calculation method
- eligible share balance
- rate
- dividend amount
- status
- calculation date
- approving officer

There may be at most one finalized dividend allocation per member and financial year. Corrections must create traceable adjustments rather than overwrite finalized allocations.

## 8. Cross-Domain Invariants

- Multi-table writes in one domain use `ExecTx`.
- Retryable inserts use `ON CONFLICT DO NOTHING` or an equivalent idempotent path.
- Decision reads across domains must be strongly consistent.
- CEEP snapshots must not be used as input for loan approval, share withdrawal or member closure decisions.
- Finalized historical records must not change when rates, exemptions or member profile details change later.
- Corrections to finalized financial records must be append-only and auditable.
- Cursor pagination on `(created_at, id)` is required for list endpoints.
- Rows must not be hard-deleted. Use `is_deleted` or terminal statuses as appropriate.

## 9. Failure Modes

### Contribution and CEEP

- Monthly CEEP snapshot missing for an active member.
- More than one finalized CEEP snapshot exists for the same member and period.
- Same contribution is submitted or processed twice.
- Receipt is marked completed before all authoritative allocations succeed.
- COM or LGOM is missing, duplicated or charged at the wrong amount.
- Fees are applied despite a valid exemption.
- Fees are applied after withdrawal effective period.
- Loan interest is calculated from current balance instead of previous balance.
- Interest is charged when previous loan balance is zero.
- Contribution amount is zero or negative.
- Payment is recorded against the wrong member or period.
- Backdated contribution changes a finalized CEEP snapshot.
- Reports include pending or failed receipts as completed.

### Loans

- Monthly loan principal is less than `principal / 36` without approved exception.
- Overpayment causes hidden or untracked credit.
- Loan balance is reduced by total monthly amount instead of principal applied.
- Interest, fees, penalties or shares are deducted from loan principal.
- Interest accrues after full repayment.
- Principal disbursed changes after approval.
- New loan overwrites a previous loan's principal or balance.
- Repayment reversal does not preserve audit history.
- First installment date is before disbursement date.
- Last installment date does not match repayment period.
- Missed installment does not create arrears or penalty.
- Concurrent payments produce incorrect balance.

### Shares and Collateral

- Total shares in reports disagree with the Shares ledger.
- Shares are added twice after contribution retry.
- Shares paid are treated as loan repayment.
- Member withdraws or pledges more shares than owned.
- Shares already pledged are offered for another loan.
- Guarantor shares are used without approval.
- Guarantor shares secure multiple loans beyond available value.
- Guarantor withdraws while shares secure an active loan.
- Reversed contribution does not reverse associated share purchase through the Shares domain.

### Loan Applications

- Application accepted for inactive, suspended or withdrawn member.
- Application stores stale copied member identity instead of using member-service verification.
- Requested loan exceeds collateral rule.
- Same collateral is pledged more than once.
- Guarantor is inactive, ineligible or has insufficient available shares.
- Applicant guarantees their own loan.
- Loan is approved without required guarantor approvals.
- Approved amount differs from disbursed amount.
- Loan is disbursed more than once.
- Rejected or cancelled application is later disbursed.

### Membership Withdrawal

- Withdrawal request accepted without required identity reference or reason.
- Member cannot be found or has already withdrawn.
- Member has outstanding loan, pending disbursement, pledged shares or guarantor obligations.
- Unpaid group dues are omitted or deducted twice.
- Deductions exceed available shares.
- Withdrawal amount becomes negative.
- Request is paid before preparation, checking and approval are complete.
- Separation of duties is violated.
- Withdrawal is processed twice.
- Withdrawal is approved but member remains active.
- Contributions continue after withdrawal effective period.

### Dividends

- Dividend allocated more than once for same member and year.
- Dividend allocated to inactive or ineligible member.
- Rate changes without authorization.
- Rate is applied to wrong balance or period.
- Current shares are used instead of finalized historical eligible balance.
- Members who joined or withdrew mid-year receive wrong eligible month count.
- Total allocated dividends exceed approved distribution amount.
- Allocation misses calculation date or approving officer.
- Correction overwrites finalized allocation instead of appending adjustment.

### System-Wide

- Duplicate requests from retries or poor connectivity.
- Concurrent updates overwrite one another.
- Partial processing leaves reports and ledgers inconsistent.
- Unauthorized users approve loans, withdrawals, exemptions or dividends.
- Records are deleted or changed without audit trail.
- Historical calculations change after rate updates.
- Imported spreadsheet rows contain missing, malformed or duplicate values.
- Currency precision or rounding differs across domains.
- Dates are assigned to the wrong contribution period or financial year.
- Reports disagree with authoritative transactions.

<!-- ## 10. Open Decisions

These must be settled before implementation:

- Whether CEEP snapshots are generated for every active member even with no contribution, or only for members with activity. This spec currently assumes every active member gets one monthly snapshot.
- Exact monthly assessment date for COM and LGOM.
- Whether withdrawal month fees apply when withdrawal happens before or after the assessment date.
- Whether monthly loan principal below `principal / 36` is ever allowed, and who can approve the exception.
- Whether dividend eligibility uses average month-end balance, average daily balance or year-end balance. This spec currently uses average finalized month-end eligible balance. -->

CEEP snapshots MUST be generated for every active member even if they have no contributions.
COM fees and LGOM fees must be deducted on every monthly contribution (assesed on the first calender day of the month)
For withdrawal fees, fees already assessed remain payable; no future fees after the effective withdrawal date
For principal below principal / 36, Prohibit during normal repayment; permit only through an approved restructuring
For the dividend balance, Use the average finalized month-end eligible balance
Existing receipt does not automatically mean successful receipt.
Existing allocation does not automatically mean complete allocation plan.
