# Hosted provider validation

The shareable Vercel client accepts a visitor's provider key only for the current browser tab. A
connection check sends one synthetic text request through the same fixed server route used by chat.
It is an integration check, not a free health endpoint: the selected provider may count or charge
for the request.

## What the check proves

A successful check proves only that, at that moment:

- the key was accepted by the selected provider;
- the provider account could access the exact selected model identifier; and
- the hosted Osanwë route could reach the provider's fixed HTTPS endpoint; and
- the provider returned readable, non-whitespace text, not just HTTP 200.

It does not prove that later requests will fit a rate limit, remain free, satisfy a provider's terms,
or receive any particular retention or training treatment.

## Safe diagnostics

Osanwë reads a bounded response to check for text, discards its content, and
reports only a bounded category:

| Code | Meaning | Retry without changing settings? |
|---|---|---|
| `invalid_key` | The provider rejected the credential | No |
| `model_access_denied` | The account cannot use the model | No |
| `credit_unavailable` | The account has no available credit | No |
| `model_unavailable` | The model is absent for this account | No |
| `provider_limit_reached` | A rate or spending boundary was reached | Later |
| `provider_timeout` | The provider did not answer in time | Yes |
| `provider_unavailable` | The provider returned a server failure | Yes |
| `provider_unreachable` | The hosted route could not reach the provider | Yes |
| `provider_output_missing` | Success status without readable output | Check model/output limit |

Upstream error bodies can contain account names, balances, internal identifiers, or provider request
IDs. They are never returned to the browser or written into the conversation.

## Release verification

Before advertising a provider or default model, test a dedicated, least-privilege key directly
against the provider and then through `/api/providers/check`. Use a synthetic prompt, record only the
provider, model, date, public result category, deployed commit, and region, and revoke the key after
the verification window. Never copy the key, prompt response, or upstream error body into an issue.

## Free-only TokenRouter verification

From the repository root, run this in PowerShell 7 after the new commit is deployed:

```powershell
pwsh -NoProfile -File web/scripts/verify-free-tokenrouter.ps1
```

The prompt hides key entry; the key is passed to the child process over stdin,
not saved to disk, exported to shell history, or put in command-line arguments.
Close any screen recording before entry. Do not run with a shell transcript.

The script verifies the exact model page still lists zero input/output pricing
and requires confirmation that this account's route is free. It uses only
`z-ai/glm-5.3-free`, at most six requests, no retries, and no model fallback:
direct readable probe, hosted readable probe, ordinary answer, synthetic
follow-up context, longer answer cancelled after first text, and an invalid-key
case. A prerequisite failure stops the run. A 32-token probe can legitimately
fail for reasoning-only output; that is not proof that a key is invalid.

The report retains pass/fail, timings, visible-character counts, provider/model,
date and public host, not keys or responses. Record the matching deployed commit
and coarse client region separately. The automated follow-up check proves
readable output, not answer correctness; the observed sessions test correctness.
No live run has been recorded for this development batch yet.

Published zero prices were verified on 2026-09-09 at the
[exact TokenRouter model page](https://www.tokenrouter.com/models/z-ai/glm-5.3-free/).
They do not guarantee future pricing, account-specific charges, or hosting costs.
