# Hosted preview verification

## 2026-09-28 diagnostic retest

Interactive HTML remains source-only. The isolated diagnostic was rerun in
Chrome 154.0.8037.57 and Edge 154.0.4258.37 using synthetic loopback probes only.
Both browsers produced the following results:

| Scenario | Native HTTP probe | Native WebRTC probe | STUN packets |
| --- | --- | --- | ---: |
| Unrestricted control | Completed, one request | Completed | 3 |
| Protected top document | Verified CSP rejection | Completed | 0 |
| Protected data-URL frame | Verified CSP rejection | Completed | 0 |
| Protected data-URL frame with nested srcdoc | Verified CSP rejection | Crashed | 0 |
| Protected blob-URL frame with nested srcdoc | Verified CSP rejection | Crashed | 0 |

Both diagnostic runs exited nonzero because the nested WebRTC scenarios crashed.
Zero traffic during a crash is not evidence of successful confinement. This
reproduces the unresolved failure on newer browsers without establishing its
root cause. The passing source-only client suite does not override this gate.

The diagnostic now requires a successful traffic-producing control separately
for HTTP and UDP. HTTP denial must combine zero listener requests, a fetch
TypeError, and a matching `connect-src` policy event. Arbitrary RTC exceptions,
missing completion, and renderer crashes all fail. Per-scenario UDP listeners
avoid attributing late packets to a later test. Reports contain fixed outcome
codes and allowlisted exception names, not exception messages or stacks. Five
unit tests cover this verdict policy. These controls strengthen the diagnostic;
they do not expand production preview permissions.

## Current hosted behavior: 2026-09-14

Generated HTML, CSS, and embedded JavaScript now load automatically as literal
source text. The hosted runner does not parse generated markup, create nested
preview frames, or execute scripts from an HTML bundle. The interface explicitly
labels this mode **Source only** and its action **Show source**. Standalone
JavaScript still runs in a disposable worker with Stop, Reload, and a timeout.
The hosted runner's response policy also blocks child frames and images.

This removes the affected execution path from the hosted client. It does not
repair the browser crash or establish safe interactive HTML execution. The
downloadable client's experimental runner has not been changed by this mitigation.
Do not restore interactive HTML or widen its distribution until its boundary has
been independently verified.

Local validation passed 87 unit tests, with two live-provider tests skipped, plus
all eight browser tests in installed Chrome and Edge. Lint and the production
build passed. Browser tests use synthetic provider fixtures, not real API keys.
They cover literal malicious markup, no nested elements or external requests,
automatic worker execution, switching modes, timeout recovery, Stop, and Reload.
These results do not prove live provider access.

## Historical 2026-09-09 findings, before the source-only mitigation

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

## Reproduce the current hosted suite without provider requests

In `web`, use the actual installed browser, not a device preset with a different
version's user agent:

```powershell
$env:OSANWE_BROWSER_CHANNEL = 'chrome' # or msedge
npm run test:browser
```

For the unresolved crash, in a disposable test browser only:

```powershell
node scripts/diagnose-preview-rtc.mjs
```

Remove the browser-channel override for the bundled-browser suite. HTML must
remain source-only on every browser. A passing source-only test is not a
successful interactive HTML test. A browser version check is a compatibility
filter, not runtime attestation that policies are enforced.

Future debugging can build on the isolated diagnostic below to determine why
the nested case fails, then test any fix against calibrated network controls.
Never fix it by allowing same-origin access, removing the connection policy,
lowering the version gate, or declaring the crash a passing security test.

## 2026-09-10 isolated diagnostic

`web/scripts/diagnose-preview-rtc.mjs` now reproduces the issue without the
Osanwë client, provider requests, or credentials. Run it with Node from `web`;
it launches a disposable headless Chrome and uses only loopback HTTP and UDP.
It exits nonzero on a crashed or incomplete protected scenario.

On Chrome 153.0.8010.36:

| Scenario | Result | STUN packets |
| --- | --- | ---: |
| Unrestricted control | Completed | 3 |
| Protected top document | Completed | 0 |
| Protected data-URL frame | Completed | 0 |
| Protected data-URL frame with nested srcdoc | Crashed | 0 |
| Protected blob-URL frame with nested srcdoc | Crashed | 0 |

This narrows the failure to the nested scenario in this browser configuration.
The zero packet count during a crash is still not a passing isolation result.
Switching the preview from a data URL to a blob URL did not remove the crash in
this diagnostic, so production framing and security policies were not weakened.
A browser-level fix or independently verified alternative is still required
before widening interactive HTML access.
