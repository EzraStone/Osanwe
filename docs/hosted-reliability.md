# Hosted beta reliability

Updated 2026-09-25. This is the hosted bring-your-own-key client, not the
independent relay network. The host and selected provider process credentials,
prompts, and answers in transit. No payment service or new infrastructure was
enabled for this work.

## Changes

- Connection checks can be cancelled. Forgetting a key or changing the selected
  provider/model invalidates an outstanding check, so an old success cannot
  overwrite the new state. Checks have a 25-second browser deadline.
- Keys are validated locally, never inserted into the URL, and cleared on page
  navigation/restoration. Credentialed fetches refuse redirects, omit cookies,
  and disable caching. Cancellation cannot recall an already sent request.
- Custom model IDs remain selectable in the composer. An unavailable or malformed
  provider catalog gives an explicit retry action without making an AI request.
- Error bodies and streamed answers have browser-side size and time limits.
  Raw hosting error pages are not displayed. Empty or interrupted answers are
  failures, not successful replies. Failed answers stay out of later context.
- Model choices support arrow, Home, and End keys. Input-method composition does
  not accidentally submit a prompt when Enter confirms a character.
- Interactive HTML remains source-only. Standalone JavaScript still uses the
  existing disposable worker. This batch does not fix the nested WebRTC crash.

## Repeatable checks

Recorded local results on September 25: 109 unit tests passed, with the two
live-provider tests skipped. All 41 browser tests passed in Chrome 154.0.8037.57
and all 41 passed in Edge 153.0.4234.48. Lint and production build passed. The
loopback deployment checker passed all three GET checks with zero provider
requests. No real-provider success, new participant session, or new regional
measurement is implied by these results.

From `web`, run `npm test`, `npm run lint`, and `npm run build`.
Run `npm run test:browser` for the synthetic end-to-end suite. To use installed
Windows browsers, set `OSANWE_BROWSER_CHANNEL` to `chrome` or `msedge` first.
The new fixture intercepts provider APIs and rejects unexpected external
destinations. Test credentials and answers are synthetic. Do not replace them
with a real key, and do not record real-key entry in a browser trace.

The suite exercises consent, credential validation/removal, provider changes,
connection-check races and retry, follow-up context, Stop/New/mode changes,
empty/truncated/network failures, opt-in history and deletion, keyboard controls,
desktop/phone opening layouts, and preview restrictions. These checks are not an
independent security audit, a native screen-reader evaluation, or a live-provider
verification. Synthetic page lifecycle events do not prove every browser's
back-forward cache behavior.

For a deployed-header check, run:

```text
node scripts/check-hosted-deployment.mjs http://127.0.0.1:3100
```

The only permitted remote target is `https://osanwe.vercel.app`. This script
performs three credential-free GETs, checks the client and runner policies, and
validates the public provider catalog. It sends no prompt or provider request.
It does not exercise account billing, edge quotas, emergency pause activation,
or model availability. A passing result does not establish a permanent $0 cap.

## Human beta checklist

Use the observed-session procedure in `hosted-beta-sessions.md`, after its entry
gates. Do not substitute automated results for participants. Testers should see
the privacy notice, load a dedicated key privately, explicitly test the
connection, receive a real answer, stop a request, and remove the key. Record
outcomes without collecting prompts, keys, or private contact information in Git.

Live TokenRouter access still needs a privately entered current key and a fresh
zero-price/account check. The word `free` in a model ID is not proof of billing
behavior. No paid fallback, paid request, plan upgrade, or purchase is authorized.
Five human sessions, additional Phase 0 regions, and an independent relay
operator remain external requirements.
