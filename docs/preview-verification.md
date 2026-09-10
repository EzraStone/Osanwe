# Hosted preview verification

## 2026-09-09 findings

Synthetic provider fixtures were used; these tests do not prove live model access.
The installed Chrome 153.0.8010.36 successfully exercised automatic JavaScript,
interactive HTML, blocked parent access, the JavaScript 2.5-second timeout, Stop,
replacement runs, and reloading the last snapshot rather than unrun edits.

A calibrated native HTTP test also passed: an unrestricted control reached a
local listener, while a newly created preview frame with native `fetch` could
not. Testing only the preview's replaced `fetch` function would not establish
that browser-level enforcement worked.

**Unresolved:** enabling native WebRTC inside the fresh nested frame crashed
the preview on Chrome 153.0.8010.36 and Edge 152.0.4191.66. The unrestricted
STUN control produced packets. The isolated run did not complete, so its network
boundary is **not verified**. This is an availability failure, not evidence of
successful confinement and not, by itself, evidence of data exfiltration. Do not
claim a root cause in Chromium until a smaller reproduction establishes it.

The crashing regression is opt-in to avoid repeatedly crashing ordinary test
runs, with an explicit skip reason in the report. Skipping it does not close the
gate. Broad distribution of interactive HTML previews remains blocked until it
passes or the implementation is replaced with a verified boundary. Basic HTML
continues to function in the existing experimental preview; its label is not a
blanket security guarantee. Use only synthetic data.

## Reproduce locally without provider requests

In `web`, use the actual installed browser, not a device preset with a different
version's user agent:

```powershell
$env:OSANWE_BROWSER_CHANNEL = 'chrome' # or msedge
$env:OSANWE_REQUIRE_INTERACTIVE_HTML = '1'
npm run test:browser -- runner-security.spec.mjs
```

For the unresolved crash, in a disposable test browser only:

```powershell
$env:OSANWE_RTC_CRASH_REPRO = '1'
npm run test:browser -- runner-security.spec.mjs --grep 'native webrtc'
```

Remove these environment overrides for the bundled-browser suite. Older engines
must visibly lock HTML and keep JavaScript available; their passing fallback
test is not a successful interactive HTML test. Run the full regression on a
supported engine before widening access. A browser version check is a
compatibility filter, not runtime attestation that policies are enforced.

Next debugging step: reduce the WebRTC case to a standalone response-policy
document and nested data/srcdoc variants, identify whether the crash is tied to
policy inheritance, then test the fix against both calibrated network controls.
Never fix it by allowing same-origin access, removing the connection policy,
lowering the version gate, or declaring the crash a passing security test.
