# Hosted provider validation

The shareable Vercel client accepts a visitor's provider key only for the current browser tab. A
connection check sends one synthetic text request through the same fixed server route used by chat.
It is an integration check, not a free health endpoint: the selected provider may count or charge
for the request.

## What the check proves

A successful check proves only that, at that moment:

- the key was accepted by the selected provider;
- the provider account could access the exact selected model identifier; and
- the hosted Osanwë route could reach the provider's fixed HTTPS endpoint.

It does not prove that later requests will fit a rate limit, remain free, satisfy a provider's terms,
or receive any particular retention or training treatment.

## Safe diagnostics

Osanwë discards the provider's response body and reports only a bounded category:

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

Upstream error bodies can contain account names, balances, internal identifiers, or provider request
IDs. They are never returned to the browser or written into the conversation.

## Release verification

Before advertising a provider or default model, test a dedicated, least-privilege key directly
against the provider and then through `/api/providers/check`. Use a synthetic prompt, record only the
provider, model, date, public result category, deployed commit, and region, and revoke the key after
the verification window. Never copy the key, prompt response, or upstream error body into an issue.
