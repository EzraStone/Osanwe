# Ox Alpha evaluation status

**Status: internal evaluation only. Not enabled for the public or private beta.**

Osanwë can technically route `stealth/ox-alpha` through OpenRouter's OpenAI-compatible API. That
does not by itself make the route suitable for a privacy product. The provider is anonymous, its
availability is temporary, and the currently published data-use statements are not clear enough
to support a user-facing privacy claim.

On 2026-08-22, the project asked OpenRouter for written clarification before exposing a pooled
Ox Alpha credential to anyone. The request asks whether an anonymous, no-charge token gateway is
a permitted product integration; which retention and training terms govern the model; whether
data controls apply; what limits and end date apply; whether synthetic benchmark results may be
published; and what language testers must accept.

## Gates before a tester can use this route

All of these must be satisfied:

1. OpenRouter confirms in writing that the proposed customer-facing integration is permitted.
2. Retention, training, and deletion behavior are documented without contradictory claims.
3. The operator records the model's provider identity, lifecycle, policy source, and last review
   date in the gateway catalog.
4. The beta disclosure names OpenRouter and the anonymous Stealth provider and forbids sensitive,
   health, financial, employment, legal, and children's data.
5. Per-invite issuance and a strict aggregate kill switch prevent one tester from consuming the
   whole preview allowance.
6. A non-sensitive smoke test passes through an independently operated relay.

Until then, the sample route in [`routes.ox-alpha.internal.example.conf`](../routes.ox-alpha.internal.example.conf)
is for local configuration validation only. Do not deploy it, distribute the credential, run
third-party prompts through it, or publish benchmark results.

## Published sources to re-check before enablement

- [OpenRouter model listing](https://openrouter.ai/stealth/ox-alpha)
- [OpenRouter terms](https://openrouter.ai/terms)
- [Stealth model EULA](https://openrouter.ai/stealth/terms)
- [OpenRouter privacy settings](https://openrouter.ai/settings/privacy)

The route must fail closed if the preview disappears or its terms change. A free price is not a
service-level guarantee, and no date should be represented as guaranteed unless the provider
confirms it in writing.
