# Five observed hosted-beta sessions

Status: PREPARED, NOT RUN. As of 2026-09-09 no testers or independent relay
operator have volunteered. Automated fixtures are not user sessions.

Recruit five willing people for 15-minute sessions: a nontechnical Windows user,
a coding-tool user, a macOS/Safari user, a Linux/Firefox user, and a keyboard-only
or screen-reader user. One person can cover multiple characteristics; record
five distinct participants using pseudonyms T01–T05. Do not publish their names
or contact details. No unsolicited invitations have been sent.

Before inviting anyone, complete the hosted gates in `release-gates.md`. Explain
that this website and the selected provider process keys and conversations in
transit. This is not an anonymous relay test. Ask for verbal consent to observe;
do not record the screen or watch key entry. Use a dedicated revocable key with
verified free-model access. Never share an operator's general-purpose key.

## Session script

Give the URL and ask the tester to work without coaching for up to two minutes
per task. Help after that point and record that assistance was needed.

1. Explain who can see a prompt and the API key, using the information on screen.
2. Open Settings, select TokenRouter and the exact verified-free model, and load
   the key privately. Run the connection check once, after confirming zero price.
3. Ask “Invent a name for a blue toy boat.” Then ask “What color was the boat?”
   Record whether the follow-up used the context, not the answer itself.
4. Request a numbered list of imaginary garden names. Stop generation, then
   send another short message. Record whether both controls recover.
5. Switch to Code and ask for a self-contained counter button. On a supported
   browser, click it, Stop preview, and Reload. On unsupported browsers, record
   the HTML restriction, then try a JavaScript console example.
6. Start New, then reload the tab. Confirm the key is no longer loaded. If local
   history was deliberately enabled, demonstrate deletion and explain its scope.

The script is a task menu, not authorization to spend. A full session uses more
than the six requests authorized for the initial verification. Confirm a separate
free-only allowance before each session; stop on a pricing/permission uncertainty.

## Minimal record

- Participant code, date, app commit/deployment, OS/browser version, input method.
- For each task: unassisted / assisted / failed / not attempted, elapsed seconds,
  and a short UI/error category. No prompts, replies, keys, screenshots, or raw logs.
- Time to first successful answer, connection-check result, context correct yes/no,
  stop/recovery result, preview result, privacy-boundary understood yes/no.
- One optional paraphrased usability observation, approved by the tester.

Initial targets: at least four of five reach a real answer without coaching within
two minutes; all five can identify the hosted privacy boundary; zero credential
exposures or silent paid fallbacks. These are acceptance targets, not measured
results. A security issue stops recruitment; a usability miss becomes a tracked
fix and re-test, not a rounded-up success percentage.
