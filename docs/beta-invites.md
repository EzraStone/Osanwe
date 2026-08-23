# Fixed-window beta invites

**Status: server and offline-generator half implemented; the shipped client cannot consume an
invite book yet. Do not open the beta from this document alone.**

The first free beta needs a bounded share for each of ten invitees without asking the gateway to
recognize them. `invitebook` prepares ten independently seeded books with a fixed number of
one-shot vouchers. The mint receives only a sorted, ungrouped set of voucher fingerprints. It can
validate and consume one voucher per blind signature, but it has no per-book identifier with which
to group an invitee's requests.

This is bounded-share fairness, not smooth rate limiting. An invitee can spend their whole book at
once, transfer it, or collude with somebody else. The gateway's aggregate request, byte, output,
and cost ceilings remain necessary.

## Privacy and recovery boundary

- Generate books offline on Linux or macOS in an owner-controlled directory that is not synced or
  version controlled. Windows generation and authorization fail closed until ACL checks exist.
- Copy only `invite-manifest.json` to the mint. Distribute each book out of band. A retained copy
  mapped to a person lets the distributor regroup that person's mint requests, so erase mapped
  copies after confirmed delivery when the recovery policy permits it.
- Use a dedicated program id and dedicated mint key for one cohort. The manifest is bound to that
  key and to a half-open, whole-second UTC issuance window.
- The receipt database is monotonic, single-writer security state. Never run two mints from cloned
  copies and never restore an older snapshot. Loss, rollback, or suspected cloning requires
  retiring the program and key, creating a fresh database, and issuing new books.
- `not_after` closes new issuance; it does not expire tokens already issued. Retire the matching
  gateway route and mint key at the rehearsed boundary. The client still needs an exact expiry and
  crash-safe reserve-before-network wallet before this is deployable.

## Operator preparation

On an offline Linux or macOS machine, get the key id for the dedicated mint key and generate a new
output directory. Replace the example dates; the end is exclusive.

```sh
eregion -key /secure/beta-mint.key -print-key-id

invitebook \
  -program beta-2026-01 \
  -mint-key-id 'mint-REPLACE_WITH_PRINTED_KEY_ID' \
  -not-before '2026-01-10T00:00:00Z' \
  -not-after '2026-01-17T00:00:00Z' \
  -seats 10 \
  -vouchers-per-invite 10 \
  -out /secure/beta-2026-01
```

Inspect the public capacity, independently calculate its worst-case provider cost, and transfer
only the manifest to the mint host. Pin the exact total on startup:

```sh
eregion \
  -key /var/lib/osanwe/beta-mint.key \
  -publish /var/lib/osanwe/beta-mint.pub \
  -invite-manifest /var/lib/osanwe/invite-manifest.json \
  -invite-capacity 100 \
  -receipts-db /var/lib/osanwe/invite-receipts.db
```

The mint refuses mixed open, payment, and invite modes; an unexpected capacity; a wrong mint key;
an unsafe manifest; or an unsupported platform. No real books should be generated until the client
wallet, rollback drill, route/key expiry drill, provider permission, and independent relay gates in
the [beta charter](beta.md) are complete.
