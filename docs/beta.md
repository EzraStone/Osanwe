# First technical beta

**Current state: recruiting and preparation, not open traffic.**

The first Osanwë beta should be ten invited people, free of charge, using synthetic or deliberately
non-sensitive prompts. It is a reliability and privacy-boundary test, not a public launch. A small
cohort makes failures diagnosable and keeps the operator able to stop the entire service before a
provider limit or privacy assumption is crossed.

## The ten seats

Recruit for roles, not audience size:

1. An independent relay operator who is not part of the Osanwë gateway operation.
2. An application-security engineer who will try to break origin, token, and secret boundaries.
3. A privacy or applied-cryptography reviewer who can assess the proposed unlinkability properties
   and their limits.
4. A heavy AI coding-tool user who can exercise streaming and long-running agent work.
5. A nontechnical Windows user who will expose installation and onboarding friction.
6. A macOS user who will test Gatekeeper, launch, browser opening, and removal.
7. A Linux developer who will test the archive, service lifecycle, and logs.
8. A keyboard-only or screen-reader user who can assess the local interface.
9. A reliability or DevOps operator who will test reconnects, failure modes, and the kill switch.
10. A privacy-conscious product tester who will challenge whether the disclosures are understandable.

Avoid recruiting people who need to submit real medical, legal, financial, employment, school, or
children's data. Do not use journalists or vulnerable-source workflows as an early proof point.

## Entry gates

The beta begins only when:

- one relay has an operator separate from the gateway operator and its key distribution path is
  documented; this enables an experimental test of the intended separation, not a privacy guarantee;
- the chosen provider has approved the exact integration and its current data-use facts are shown
  in the model catalog;
- the client has checksummed downloads and launches its local interface without requiring a build
  toolchain; if paid platform signing is deferred, the unsigned status and safe OS-warning path are
  documented plainly;
- a clean Windows, macOS, and Linux install has been exercised;
- incomplete streams, cancellation, local storage failures, and relay verification are shown
  truthfully in the interface;
- aggregate daily and minute limits and anonymous per-epoch invitation issuance are enabled, and
  the operator has rehearsed disabling the route; the [invitation wallet](beta-invites.md) is
  implemented, but this gate remains incomplete until the live migration and drills are recorded;
- the beta agreement says it is free, experimental, text-only, and unsuitable for sensitive data,
  and requires testers to acknowledge that the configured model provider receives prompts and
  answers under the gateway account;
- Phase 0 has three-region evidence or the product is explicitly positioned for async and agentic
  work rather than interactive chat.

Provider permission, security review, and privacy correctness outrank any free-credit or preview
deadline. Missing a temporary credit costs convenience; rushing an unresolved data or money path
can permanently cost trust.

## Website, hosted preview, and local app boundary

The public promotional website explains the project, publishes downloads and checksums, shows
current limitations, and collects beta interest. The current interest link requires GitHub sign-in
and creates a public issue tied to the applicant's GitHub username; that identity disclosure must
appear beside the call to action. The promotional site has no prompt field or application path that
receives prompts, API keys, tokens, or local history.

A separately labeled hosted BYOK preview may provide a low-friction compatibility test. Its hosting
platform necessarily processes the visitor's provider key, prompt, and answer in plaintext at
runtime. It is not the relay path, does not use blind-signed tokens, and must never be presented as
evidence that the relay privacy properties are working. The privacy beta chat remains in the
downloadable client on `127.0.0.1`; its launcher starts the local client and opens that loopback page.

Until checksummed archives, documented unsigned OS-warning paths, and an approved provider route
exist, the promotional website must say **Join the technical beta**, not **Start chatting**. The
hosted compatibility path may say **Try the hosted BYOK preview** only beside its runtime disclosure.

## What each tester does

Each seat gets a short, role-specific checklist plus the same core run:

1. Install from a checksummed archive and follow the documented warning path if that beta build is
   unsigned or not notarized.
2. Confirm the relay and model disclosures before sending anything.
3. Run five supplied synthetic prompts, including one long stream and one cancellation.
4. Disconnect the relay during a request and record the failure shown.
5. Export or delete local history and verify the stated result.
6. Complete a ten-minute comprehension survey without sending prompt text back to the operator.

Feedback should contain version, operating system, timings, and error categories. It should never
contain a tester's prompt or response unless they knowingly used the supplied synthetic fixture.
The [technical beta report form](https://github.com/EzraStone/Osanwe/issues/new?template=beta-report.yml)
requires GitHub sign-in and creates a public, username-linked issue; use it only for redacted
ordinary results. Suspected vulnerabilities go through the repository's private Security tab, not
a public issue.
