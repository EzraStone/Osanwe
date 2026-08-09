# BTCPay token sales

Osanwe's first production authorizer uses a self-hosted [BTCPay Server](https://docs.btcpayserver.org/).
It is non-custodial and removes a conventional payment processor from the purchase path. That is a
better match for the network than sending every buyer's identity and purchase history to a card
processor. Bitcoin and Lightning have their own privacy limitations; this choice does not make a
payment anonymous by itself.

The privacy boundary is the blind signature. `eregion` verifies a payment and signs a blinded
message. The invoice ID is then durably consumed. The token eventually presented to `mithlond`
contains neither that invoice ID nor anything BTCPay returned, so neither the mint nor BTCPay can
recognize it later.

## Configure BTCPay

1. Run a BTCPay Server instance you control and create a store.
2. Create a store-scoped Greenfield API key with only **View invoices**
   (`btcpay.store.canviewinvoices`). Do not grant wallet, refund, webhook, or server permissions.
3. Create invoices for exactly the amount and currency configured on `eregion`. A BTCPay public
   form with constant `invoice_amount` and `invoice_currency` fields is one simple first checkout.
4. Give the settled invoice ID to the buyer as the one-time receipt. The current client accepts it
   through `OSANWE_RECEIPT`.

BTCPay's API-key authentication and least-privilege permissions are documented in its
[Greenfield authorization guide](https://docs.btcpayserver.org/BTCPayServer/greenfield-authorization/).
Do not put buyer names, email addresses, postal addresses, or Osanwe account identifiers in invoice
metadata. Osanwe needs only the opaque invoice ID, exact price, currency, store, and settlement
status.

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

The authorizer is the security boundary, not a complete checkout product. A public launch still
needs a buyer-facing flow that creates fixed-price invoices and returns their IDs, HTTPS in front of
the loopback mint, documented refund/support handling, API-key rotation, and operational monitoring
that does not log issuance requests. `-open` remains exclusively for local demos.
