# billing

Accounts that pay: card payments and a prepaid credit wallet with the
`payment` plugin, sign-up and sign-in with the `auth` plugin. There is no
hand-written Go: both plugins are activated in
`proto/domains/billing/w17.proto` and their endpoints mounted on one REST API.

- **Payments** (`POST /api/v1/billing/payments`, `GET …/payments/{id}`): a
  payment starts unsettled and the provider settles it by webhook. Every
  endpoint acts for the signed-in caller only; another person's payment reads
  as not found.
- **Credit** (`POST /api/v1/credit/top-ups`, `GET /api/v1/billing/credit/balance`):
  a top-up is a card payment whose amount is credited when the provider
  confirms it. The top-up endpoint is the project's own choice, declared in
  `proto/domains/billing/rest.proto`; spending credit is left to the project's
  own code (`PaymentService.SpendCredit`).
- **Webhooks** (`POST /api/v1/webhooks/stripe`): no account, but every delivery
  must carry the provider's signature over the exact body.

Unlike the other examples, this one pins its plugins to a commit on the
plugins repository's `main` (`w17/lock.yaml`), not a release, until a
release carries the fixes it needs.

## The provider

`fakestripe/` is a small stateful stand-in for the part of the Stripe API the
plugin calls, so the example runs without a Stripe account. Unlike Stripe's
own mock it keeps what it creates and enforces idempotency keys and the refund
cap. `compose.billing.yaml` points the plugin at it with test values; the plugin
refuses a live key (`sk_live_…`) anywhere but the real API.

## Tests

```sh
w17ctl codegen
w17ctl test          # the generated end-to-end suite (w17/e2e/billing/)
livetest/run.sh      # the live suite
```

The end-to-end suite checks the API: sign-up and sign-in, payments and their
refusals (a bad amount, paying for someone else, reading someone else's
payment), an idempotent retry, a top-up, and unsigned or forged webhooks.

`livetest/` checks what only a running stack can show, with a real database
and concurrent requests:

- a signed webhook settles a payment; eight concurrent copies of one event
  are all acknowledged and change it once; a late failure does not undo a
  success; an unsigned, forged, stale or altered delivery changes nothing;
- the gateway hands the plugin the webhook body byte for byte;
- an event that arrives before its payment's row is refused, so the provider
  delivers it again;
- a top-up is credited once however many times it is confirmed;
- concurrent spends never take a balance below zero, and a refused spend
  leaves no trace;
- a user's concurrent first payments share one provider customer, and two
  people may use the same order number as their idempotency key.
