# Anonymous daily beta invitations

**Status: implemented and locally testable. Do not open the beta until the deployment, provider
permission, release-candidate, and independent-relay gates in the beta charter are complete.**

The first free beta is designed for ten invitees, five requests per UTC day, for seven days. The
mint receives only sorted voucher fingerprints grouped by a shared daily epoch. It learns that a
voucher belongs to today's cohort allowance, but receives no book, seat, email, account, or stable
caller identifier with which to group two issuance requests.

This bounds anonymous issuance, not human identity. An invitation can be shared, and a modified
client can issue today's tokens without using them until later. Preventing stockpiling would require
epoch-bound tokens visible to the gateway, which would add linkability and a more complicated key
rotation protocol. The first beta accepts that limitation and keeps the gateway's independent daily
and minute ceilings mandatory.

## What the client now does

- **Settings → Free test access** imports the separate invitation JSON into a local wallet.
- The invitation seed and unspent tokens are stored in a bbolt database inside the current user's
  configuration directory. They are not put in `osanwe.json`, browser storage, logs, or requests to
  the relay, gateway, or Groq.
- A voucher is marked reserved in an ACID transaction before the blind-signature request begins.
  A crash or ambiguous response can lose one free voucher; it cannot retry one entitlement against
  a different blinded message.
- The wallet database is exclusively locked, so a second local client cannot race the first.
- Tokens removed for a request are persisted before use. A token returns to the wallet only when it
  was never presented or the authenticated gateway explicitly says it was rejected/refunded.
- The local status endpoint exposes only activation state, counts, and public UTC boundaries.

The local database is sensitive bearer material. User-profile filesystem protection is the current
boundary; application-layer encryption and OS keychain wrapping are not implemented. Malware or
another process running as the same user can steal it.

## Generate the ten books offline

Use Linux or macOS in an owner-controlled directory that is neither synced nor version controlled.
Windows generation and mint authorization still fail closed because trustworthy owner/ACL checks
for operator authorization files are not implemented there. The Windows client can safely import a
book generated elsewhere.

Replace the example dates and key id. The window must divide exactly into the declared epochs.

```sh
invitebook \
  -program beta-2026-09 \
  -mint-key-id 'mint-REPLACE_WITH_PRINTED_KEY_ID' \
  -not-before '2026-09-01T00:00:00Z' \
  -not-after '2026-09-08T00:00:00Z' \
  -seats 10 \
  -vouchers-per-invite 35 \
  -vouchers-per-epoch 5 \
  -epoch-duration 24h \
  -out /secure/beta-2026-09
```

The result is one public `invite-manifest.json` with 350 fingerprints and ten secret files under
`books/`. Each epoch contains 50 sorted fingerprints—enough to enforce the shared 5-per-book/day
construction without preserving seat groupings.

Copy only the manifest to the mint. Deliver one secret book separately from the non-secret
`osanwe.json` enrollment. A retained mapping from a book to a person lets the distributor regroup
that person's mint requests, so erase mapped copies after confirmed delivery when the recovery
policy permits it.

## Start the dedicated beta mint

Use a new key, program, and receipt database. Pin the exact capacity so the service refuses a stale
or accidentally regenerated manifest.

```sh
eregion \
  -key /var/lib/osanwe/beta-mint.key \
  -publish /var/lib/osanwe/beta-mint.pub \
  -invite-manifest /var/lib/osanwe/invite-manifest.json \
  -invite-capacity 350 \
  -receipts-db /var/lib/osanwe/invite-receipts.db
```

Never run two mints from cloned receipt databases and never restore an older snapshot. Loss,
rollback, or suspected cloning retires the program and mint key and requires new books.

## Match the gateway to the free Groq project

The project and route both allow only `openai/gpt-oss-20b`. The API key stays in the gateway's
private environment. Apply a daily request ceiling and a separate minute ceiling before token
redemption:

```sh
mithlond \
  -routes /var/lib/osanwe/routes.groq.conf \
  -mint-key /var/lib/osanwe/beta-mint.pub \
  -spent-db /var/lib/osanwe/spent.db \
  -budget-db /var/lib/osanwe/daily-budget.db \
  -budget-window 24h \
  -budget-requests 50 \
  -budget-input-bytes 200000 \
  -budget-output-tokens 50000 \
  -burst-budget-db /var/lib/osanwe/minute-budget.db \
  -burst-budget-window 1m \
  -burst-budget-requests 5 \
  -burst-budget-input-bytes 32768 \
  -burst-budget-output-tokens 8000 \
  -max-output-tokens 1024 \
  -cert /var/lib/osanwe/gateway.crt \
  -key /var/lib/osanwe/gateway.key
```

These values mirror the currently configured project caps; verify the Groq console immediately
before deployment because provider limits can change. A gateway ceiling refuses before token
spend. If Groq itself returns HTTP 429 after dispatch, Osanwë replaces provider-specific details
with a clear capacity message and truthfully marks the one-shot token spent.

`not_after` closes new issuance. The Groq key, mint key acceptance, and model route must retire at
the same or earlier boundary. Rehearse that shutdown before distributing any real book.
