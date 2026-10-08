# CEEP Domain Specification

Status: requirements baseline, recorded 8 October 2026. This document preserves the user's CEEP requirements and April 2026 monitoring report for Our Lady of Consolata Sagana, Kabiruini Parish. Payment date: 27 April 2026; printed: 22 May 2026. Report month, payment date and generation date are distinct.

Read this before implementing CEEP or charge extensions. It supplements [the cross-domain spec](caritas-final-domain-spec.md), [Shares](caritas-shares-domain-spec.md) and [Loans](caritas-loans-domain-spec.md). Confirmed requirements, proposed interpretations and implementation gaps are distinguished below.

## 1. Domain boundaries

Members owns identity, member number, name, branch and lifecycle. Shares owns share accounts, transactions and historical balances. Loans owns disbursed principal, principal balances, repayments, interest, loan penalties and overpayment credit. Contributions coordinates receipts and COM/LGOM allocations. CEEP consumes authoritative records; it never creates or corrects share or loan balances. Contributions owns manually assessed miscellaneous member charges and non-loan penalties. Loan penalties remain owned by Loans.

The existing cross-domain spec requires one monthly snapshot for every active member, including members without payments. Finalized snapshots are immutable, have a declared cutoff and use completed authoritative records. Pending, failed and partial receipts must be separately reconciled rather than reported as completed contributions.

## 2. Period and source records

Use calendar months in Africa/Nairobi, canonically represented as the first day, for example 2026-04-01. Identify branch/parish, period, cutoff and generation time. Aggregate multiple receipts per member/month. Preserve member IDs, receipt references and owning-domain transaction IDs.

Opening balances must come from owning-domain history at the beginning of the reporting month, not current balances. A later correction must not silently rewrite a finalized snapshot.

## 3. Field dictionary

| Source column | Meaning and authoritative source |
|---|---|
| RecNo | Legacy record/receipt reference, such as B08132. Exact meaning and display for multiple receipts require confirmation. |
| M.NO | Member number from Members; preserve leading zeros. |
| FullName | Member name from Members, captured in the snapshot. |
| COM | Monthly COM payment. Standard assessment: KES 30. Keep assessment, payment and unpaid balance distinct. |
| Previous shares | Opening share balance from Shares. |
| Shares paid (Sharespa id) | Shares added through contributions that month, backed by completed Shares purchases. |
| T. shares | Previous shares plus shares paid, subject to other ledger movements below. |
| LGOM | Monthly LGOM payment. Standard assessment: KES 30. Keep assessment, payment and unpaid balance distinct. |
| Sale of literature | Separate literature payment category; rate and trigger unresolved. |
| CARITAS registration | Separate registration category. The source appears to distinguish AS Reg and UP Reg; preserve this distinction until clarified. |
| LSF | Separate payment category; full meaning and rate unresolved. |
| Principal loan disbursed | Original actual principal disbursed from Loans; static for that loan, not current balance or requested principal. |
| Monthly loan paid | Loan amount paid during the month. Proposed report interpretation: principal paid, with interest shown separately. |
| Loan balance | Previous/opening outstanding principal from Loans. |
| Interest 1% | Current-month interest assessed by Loans: 1% of previous principal balance. Record interest actually paid separately for reconciliation. |
| Loan balance after pay | Closing outstanding principal from Loans. Never original principal minus only this month's payment. |
| Laptop | Separate payment category; obligation and installment rules unresolved. |
| Penalty | Penalty actually paid. Preserve assessed and outstanding amounts separately; rate and trigger unresolved. |
| TotalAmt | Actual completed contribution amount paid that month, reconciled to allocations. |

A column in the paper report does not establish an approved charge rate or assessment policy.

## 4. Confirmed formulas

Use exact decimal KES amounts, never binary floats. Current interest assessment rounds to four decimal places; display rounding must not alter stored amounts.

```text
total_shares = previous_shares + shares_paid
current_month_interest_due = previous_loan_principal_balance * 0.01
base_total_paid = loan_principal_paid + loan_interest_paid + shares_paid
                  + COM_paid + LGOM_paid
total_paid = base_total_paid + literature_paid + registration_paid
             + LSF_paid + laptop_paid + penalties_paid
             + other_approved_charges_paid + overpayment_credit_allocated
```

The user's simplified total assumes interest is fully paid and additional charges are absent. Interest due can differ from interest paid because of partial payments or arrears. TotalAmt includes payments, not unpaid assessments.

Count loan principal, interest and credit once. Do not add the combined loan payment again after adding its components. Credit created from excess cash is already part of that payment.

The simple share formula assumes no withdrawals, dividends, adjustments or reversals during the month. If these exist, reconcile the authoritative closing Shares balance with all movements and show those movements explicitly without relabeling them as purchases.

The simple loan closing formula assumes no new disbursements or principal corrections during the month. Reconcile such movements explicitly to the authoritative closing Loans balance.

## 5. Loan closing balance and source example

Confirmed: interest uses previous outstanding principal, not original disbursed principal.

Proposed interpretation, discussed but not explicitly confirmed: Monthly loan paid displays principal paid and interest appears separately.

```text
closing_principal = opening_principal - principal_paid
```

If Monthly loan paid displays the combined repayment instead, obtain principal paid from Loans' actual allocation breakdown. Without penalties or credit, principal paid equals combined loan payment minus interest paid. Never subtract interest payments from principal.

The first source row appears to contain:

| Component | KES |
|---|---:|
| Original disbursed principal | 400,000.00 |
| Previous loan balance | 312,965.00 |
| Monthly principal paid | 15,070.00 |
| Interest at 1% | 3,129.65 |
| Shares paid | 1,000.00 |
| COM | 30.00 |
| LGOM | 30.00 |
| Total paid | 19,259.65 |

Assuming no arrears or other principal movements, combined loan payment is 18,199.65 and closing principal is 297,895.00. The printed 384,930 appears to equal 400,000 minus 15,070, incorrectly ignoring previous repayments. These transcribed figures are explanatory examples, not imported ledger records.

## 6. Current backend behavior

References: proto/contribution/v1/contribution.proto; internal/contribution/fees.sql, fees.go, cash.go and service.go; internal/loan/interest.sql and service.go (RecordRepaymentForPeriod and allocateRepayment).

1. Contributions supplies unpaid COM/LGOM when omitted. Explicit fee amounts must match unpaid assessments. Subsequent receipts do not charge already paid monthly fees again. Cross-domain policy assesses fees once per applicable member/month with approved exemptions.
2. Principal and interest request entries for the same loan are combined. Normalized LOAN_PRINCIPAL represents the combined payment, not an instruction to skip interest.
3. Loans assesses interest on original principal less principal payments from earlier contribution months. Current-month repayments reduce next month's interest. Code uses each loan's stored rate; the stated CEEP requirement is 1%.
4. Monthly assessments are unique. Later repayments catch up missing months after the latest assessment without compounding unpaid interest. Legacy loans without assessments start at their first collected period rather than receiving retrospective charges. New disbursements currently create an initial assessment.
5. Unpaid interest equals assessed interest less interest already paid. Current allocation order is interest, principal, then overpayment credit. A payment below unpaid interest leaves principal unchanged.
6. Loans records principal, interest, penalty, credit and period in allocation_breakdown. Penalty is currently zero. CEEP reads this actual split; the cash response does not expose it.
7. Future periods, periods before disbursement and backdating before a later assessed or paid month are rejected. Repeated payment identifiers are idempotent; conflicting amounts are rejected.

The Loans spec describes penalty -> interest -> principal -> credit. Current code does not implement penalty allocation. This is a policy/implementation gap to resolve before penalty work, not a decision made by this document.

## 7. Actual cash and request allocations

The top-level request amount is actual cash received. Backend fee allocation does not create extra cash or automatically reduce caller-selected shares/loan amounts.

```text
received_amount = requested_allocations + unpaid_COM + unpaid_LGOM
```

Assuming unpaid fees total 60:

| Received | Shares | Combined loan payment | Fees | Outcome |
|---:|---:|---:|---:|---|
| 10,000 | 4,000 | 5,940 | 60 | Balanced |
| 10,000 | 4,000 | 6,000 | 60 | Allocation total mismatch |
| 10,060 | 4,000 | 6,000 | 60 | Balanced |

If fees are already paid, allocations can use the full received amount. The client needs applicable unpaid fees before distributing fixed cash. A preview/quote contract is a future consideration; this document does not claim an endpoint exists. Server validation is authoritative.

Cash requests contain idempotencyKey, sessionId, memberId, amount, contributionPeriod and allocations. Branch and cashier come from authentication. Shares target a share account UUID; loans target a loan UUID. Retries reuse the same key and details.

## 8. Implemented member charges and deferred loan penalties

Contributions now stores immutable charge obligations and uses completed contribution allocations as their payments. Categories are literature, caritas_registration, lsf, laptop, penalty (non-loan), and other. There is no separate payment ledger or mutable paid-balance column. Assessments are manually entered with a required reason and amount; this does not establish automatic rates or triggers.

Before automating these assessments, define category meanings, triggers, applicability, rates, effective dates, installment rules and approvals. AS Reg and UP Reg are not separately coded until their meaning is confirmed; retain details in the assessment reason. Corrections, waivers and reversals are deferred; no charge edit/delete endpoint exists.

Loan penalties belong to Loans and remain deferred. Non-loan penalties use Contributions charge records. CEEP consumes completed charge allocations joined to their categories; generating a report must not assess charges. A loan UUID cannot be paid through the non-loan PENALTY allocation.

## 9. Acceptance scenarios

- Multiple receipts aggregate without duplicate monthly fees or interest assessments.
- Active members without payments still have snapshots; payment columns are zero while balances and obligations remain distinguishable.
- A 6,000 combined loan payment with 1,000 unpaid interest records 1,000 interest and 5,000 principal.
- A payment below interest due leaves principal unchanged and interest outstanding.
- Original disbursed principal remains visible while opening/closing balances change with repayments.
- Shares, interest, fees and penalties do not reduce principal unless Loans records a principal payment.
- TotalAmt reconciles to completed receipts without counting combined payments and their split twice.
- Historical reports use historical balances and declared cutoffs; corrections remain traceable and finalized snapshots immutable.
- Multiple loans retain per-loan detail; never select an arbitrary loan for a member row. Printed aggregation remains unresolved.

## 10. Open decisions

1. Confirm Monthly loan paid means principal paid and decide display of interest due, paid and arrears.
2. Confirm the 1% rule across loan products; code currently uses stored loan rates.
3. Define AS Reg, UP Reg, LSF and laptop obligations and rates.
4. Define penalty triggers, rates, ownership and allocation priority.
5. Decide presentation for partial fees, credits, other share movements and multiple loans.
6. Define RecNo when a member has several receipts in one month.

These questions do not reopen the confirmed COM/LGOM amounts, previous-balance interest basis, domain ownership or actual-cash rule.

## 11. Charge API and posting contract

Create an obligation with POST /api/v1/contributions/charges:

```json
{
  "idempotencyKey": "literature-member-unique-reference",
  "memberId": "<member-uuid>",
  "category": "literature",
  "amount": { "currencyCode": "KES", "units": "500", "nanos": 0 },
  "reason": "Literature supplied"
}
```

Creation uses the authenticated branch and staff identity. The member must exist in that branch. Identical retries return the original charge; changed details with the same key are rejected. Amounts must be positive and representable exactly at four decimal places. Each obligation requires a reason; the other category must not be used without describing the charge.

GET /api/v1/contributions/charges?memberId=<uuid>&pageSize=50 returns the member's charges in the authenticated branch with assessed, paid and outstanding amounts. pageToken uses a cursor on (created_at, id). Default page size is 50 and maximum is 100. Settled charges remain visible for audit.

Pay a charge through an existing cash or STK contribution allocation:

```json
{
  "type": "CONTRIBUTION_ALLOCATION_TYPE_OTHER_CHARGE",
  "targetId": "<charge-uuid>",
  "amount": { "currencyCode": "KES", "units": "200", "nanos": 0 }
}
```

Use CONTRIBUTION_ALLOCATION_TYPE_PENALTY for a charge whose category is penalty. OTHER_CHARGE cannot bypass that distinction. Partial payments are allowed. Payments above the outstanding balance, repeated targets, wrong members, wrong branches and missing charge targets are rejected. The receipt amount still includes COM/LGOM and every selected allocation.

All referenced charges are locked in UUID order before posting, then checked against completed allocation payments. Their completion and balance effect share the existing receipt transaction. No outstanding balance is maintained separately. Completed allocations count as settlement even if another allocation leaves the overall receipt failed; such receipts remain a reconciliation exception and are excluded from completed CEEP totals.

STK initiation validates the current balance but does not reserve it. A callback rechecks the balance before posting. If another payment settled the charge after the prompt, collected STK money is retained as a failed receipt for reconciliation rather than disappearing or overpaying the obligation.

The existing cash-record permission authorizes charge creation; authenticated readers can list charges in their own branch. This implementation does not add automatic assessment schedules, configurable rates, a preview endpoint, loan penalty allocation, or CEEP snapshot generation.

### Integration checks for CI (not run locally)

Run the PostgreSQL/Docker-backed tests:

- TestContributionChargePaymentsIntegration: migration application, idempotent assessment creation, cash/STK partial payments, non-loan penalties, monthly fees once, cursor listing, branch isolation, overpayment rollback and a stale STK callback retained for reconciliation.
- TestConcurrentContributionChargePaymentsIntegration: concurrent receipts cannot exceed the assessed obligation.
- Existing TestCashAndSTKMonthlyCharges and TestConcurrentReceiptsDoNotCollectMonthlyFeesTwice: regression checks for fee and loan contribution behavior.

Command:

```text
go test ./internal/contribution -run '^(TestContributionChargePaymentsIntegration|TestConcurrentContributionChargePaymentsIntegration|TestCashAndSTKMonthlyCharges|TestConcurrentReceiptsDoNotCollectMonthlyFeesTwice)$' -count=1
```