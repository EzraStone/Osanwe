# Disabled BTCPay token-sales path

**Status: implemented as disabled pre-launch code. The free ten-person beta does not accept payments
or use this path. Do not expose it or accept real money before the gates under Remaining product work
are complete.**

The authorizer prototype uses a self-hosted [BTCPay Server](https://docs.btcpayserver.org/). A
correctly self-hosted configuration can avoid a conventional card processor in the purchase path,
rather than sending every buyer's identity and purchase history to one. Bitcoin and Lightning have
their own privacy limitations; this choice does not make a payment anonymous by itself.

The narrow cryptographic boundary is the blind signature. `eregion` verifies a payment and signs a
blinded message, then durably consumes the invoice ID. The token eventually presented to `mithlond`
contains neither that invoice ID nor anything BTCPay returned, preventing a direct cryptographic
match to the blinded issuance transcript. Timing, invoice and issuance metadata, small anonymity
sets, or collusion can still correlate a purchase with later use.

## Configure BTCPay

1. Run a BTCPay Server instance you control and create a store.
2. Create one store-scoped Greenfield API key for `checkout` with only **Create invoice**
   (`btcpay.store.cancreateinvoice:STORE_ID`).
3. Create a different store-scoped key for `eregion` with only **View invoices**
   (`btcpay.store.canviewinvoices:STORE_ID`).
4. Configure the same exact amount, currency, store, and BTCPay origin on both processes.

Do not give either key wallet, refund, webhook, user-management, or server permissions. Keeping the
processes and keys separate means a checkout compromise cannot inspect existing invoices and a mint
compromise cannot create them.

BTCPay's API-key authentication and least-privilege permissions are documented in its
[Greenfield authorization guide](https://docs.btcpayserver.org/BTCPayServer/greenfield-authorization/).
Do not put buyer names, email addresses, postal addresses, or Osanwe account identifiers in invoice
metadata. Osanwe needs only the opaque invoice ID, exact price, currency, store, and settlement
status.

## Run the checkout

```bash
export OSANWE_BTCPAY_CREATE_API_KEY='<store-scoped create-invoice key>'

checkout \
  -addr 127.0.0.1:8446 \
  -btcpay https://pay.example \
  -btcpay-store STORE_ID \
  -amount 1.00 \
  -currency USD \
  -max-invoices-per-minute 30
```

Put an HTTPS reverse proxy in front of the loopback listener. The checkout creates only this
server-configured fixed-price product: its API accepts an empty JSON object, not buyer-selected
amounts or metadata. It has no accounts, cookies, analytics, third-party page resources, or CORS.
Its invoice ceiling is global rather than per-IP so enforcing it does not require a buyer identity
database. The ceiling is intentionally in memory; a restart resets it, and multiple instances have
independent ceilings.

In a closed operator test of the disabled path, checkout displays the invoice ID. The tester saves
that one-shot bearer receipt, settles a non-value test invoice in the test environment, and supplies
it to the client:

```bash
export OSANWE_RECEIPT='<settled BTCPay invoice ID>'
bearer -mint https://mint.example -mint-key-id mint-... \
       -relay relay.example:8443 -pin sha256/... \
       -upstream https://gateway.example:8444
```

One settled invoice currently entitles exactly one blind signature and therefore one inference
request. The client marks a non-empty receipt as presented before contacting the mint and will not
use it as a refillable balance. After that token is spent, a new settled invoice is required.

This fail-closed behavior is intentionally inconvenient. If the mint durably claims an invoice and
the response is lost before the client receives the signature, the current protocol cannot safely
retry with a different blinded message. Solving that crash window needs an independently reviewed,
idempotent issuance design; increasing a wallet batch size does not solve it.

## Run the mint

```bash
export OSANWE_BTCPAY_API_KEY='<store-scoped view-invoices key>'

eregion \
  -addr 127.0.0.1:8445 \
  -key /var/lib/osanwe/mint.key \
  -publish /var/lib/osanwe/mint.pub \
  -btcpay https://pay.example \
  -btcpay-store STORE_ID \
  -btcpay-amount 1.00 \
  -btcpay-currency USD \
  -receipts-db /var/lib/osanwe/receipts.db
```

`eregion` accepts only an exact `Settled` invoice from that store at that price. It never follows
HTTP redirects with the API key. The receipt database uses an atomic transaction, so concurrent
attempts to reuse one invoice produce one signature, and the decision survives restarts.

Keep `receipts.db` mode 0600 on a local filesystem and back it up with the mint's live state.
Restoring an older copy revives invoice entitlements consumed after the backup. The embedded store
is deliberately single-process and single-host; a multi-host mint needs a shared implementation of
`mint.ReceiptStore` with an atomic insert-if-absent operation.

## Remaining product work

The checkout and authorizer are an implemented but disabled payment prototype, not an audited
commerce system. A paid beta or public launch still needs HTTPS in front of the loopback checkout
and mint, documented
refund/support handling, API-key rotation, backups, availability monitoring that does not record
buyer or issuance identifiers, an idempotent recovery design for interrupted issuance, and
independent security review. `-open` remains exclusively for local demos.
