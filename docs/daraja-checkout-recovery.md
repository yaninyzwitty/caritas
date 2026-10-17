# Restore a missing Daraja checkout ID

Use this when Daraja accepted an STK request but saving its checkout ID failed.
Take the payment request ID and checkout ID from the **same backend log entry**.
Do not infer a match from the phone number, amount, or timing.

Send an authenticated `POST /api/v1/contributions/recover-daraja` using a staff
token with cash approval permission. The request must belong to that staff
member's branch.

For the save failure logged on 16 October 2026:

```json
{
  "paymentRequestId": "423fa1d2-773f-44ba-a399-a28d897acd24",
  "action": "restore_checkout",
  "checkoutRequestId": "ws_CO_161020261542329768108321",
  "reason": "Restore checkout from the backend save failure log dated 2026-10-16 15:42:46"
}
```

The action locks the payment request and requires it to be pending with no
receipt. It rejects a different stored checkout ID or one already assigned to
another request. In one transaction, it records the officer, reason and checkout
ID, restores the missing ID, schedules a provider query immediately, and requeues
exhausted callbacks for that checkout. Repeating the same restoration leaves the
checkout and query retry budget unchanged.

The response contains the stored payment under `payment`. A successful response
means recovery was scheduled; it does not mean payment succeeded. The recovery
worker runs every five seconds and uses bounded query retries. Check the payment
again through the existing status flow. Provider success without receipt metadata
still needs receipt confirmation or reconciliation using verified business records.

This action never sends an STK prompt, creates a receipt, or marks money received.
It does not fix database connectivity; resolve persistent database timeouts first.
