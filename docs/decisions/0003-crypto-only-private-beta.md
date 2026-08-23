# ADR 0003 — Keep the private beta crypto-only

- **Status:** Accepted for a private beta
- **Decides:** which payment rails are intentionally supported first
- **Supersedes:** nothing
- **Revisit when:** a legal entity and card-processor posture are chosen

## Context

The product goal includes card, cryptocurrency, Monero, cash, and voucher access. The implemented
adapter is self-hosted BTCPay. Adding cards is not another protocol integration: it normally adds a
processor relationship, legal-entity requirements, identity checks, chargebacks, and new links
between a payer and a purchase.

The money path has not yet received an independent review. Expiring infrastructure credit is worth
less than confidence that a settled invoice cannot issue twice.

## Decision

The first private beta is deliberately crypto-only through BTCPay. It will not advertise card, cash,
Monero, or vouchers as available.

BTCPay is one entitlement adapter, not the payment architecture. The mint accepts a narrowly defined,
one-use entitlement and blindly signs a token request. Future adapters must end at that boundary
without adding payer identity to issuance or inference requests.

No real-money beta opens until:

1. concurrent and restart issuance tests prove one settled invoice cannot produce two signatures;
2. checkout, entitlement persistence, and blind issuance receive a focused external review;
3. operational key rotation and recovery are rehearsed; and
4. the person authorizing launch explicitly accepts the remaining findings.

## Consequences

- Card access is deferred, not forgotten.
- A card processor, legal entity, KYC posture, refund policy, and chargeback budget require a later
  business decision.
- Monero needs a separate adapter and privacy review; BTCPay support must not be assumed.
- Cash access is expected to use prepaid voucher codes distributed without an online identity, but
  issuance, theft, denomination, and redemption rules remain undesigned.
- Infrastructure-credit expiry never shortens a money-path review. Losing credit costs convenience;
  issuing twice against one payment costs trust.
