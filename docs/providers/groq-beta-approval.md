# Groq approval gate for the invited beta

**Status: request prepared, not sent. Do not create the final gateway key or invite testers until a
human has sent this request and retained Groq's written answer.**

The current plan uses one free-tier Groq project as an operator-owned pooled route for ten invited,
accountless Osanwë testers. The gateway, not each tester, holds the key. Before opening that route,
ask Groq to confirm this exact use rather than inferring permission from general API terms.

## Message to send

> Subject: Permission question for a small accountless Osanwë technical beta
>
> I am preparing a free, seven-day technical beta of Osanwë for ten invited testers. Osanwë is an
> encrypted-relay client and gateway for text-only AI inference. Testers will not receive my Groq
> API key or create Groq accounts. A gateway I operate will hold one project-scoped key and forward
> at most 50 requests per day to only `openai/gpt-oss-20b`.
>
> The project is on Groq's Free plan with no paid upgrade requested. Project limits are set to 5
> requests/minute, 50 requests/day, 8,000 tokens/minute, and 100,000 tokens/day. Zero Data
> Retention for inference APIs is enabled. Osanwë also enforces durable daily and minute ceilings,
> five anonymous issuance vouchers per tester per UTC day, a seven-day route/key expiry, and no
> fallback provider or model. Test prompts are restricted to synthetic or deliberately
> non-sensitive text.
>
> Does Groq permit this accountless, operator-keyed invited beta under the Free plan? If yes, please
> also confirm any required attribution or tester disclosure, whether ZDR applies to this traffic,
> whether request/response content may be used for training or abuse review despite ZDR, and whether
> there are lifecycle or redistribution restrictions specific to `openai/gpt-oss-20b`.
>
> I will not open the beta until I receive written confirmation. Thank you.

## Record with the answer

- Date, support channel, case/message id, responder name or team, and exact answer.
- Any required public attribution or tester language.
- The policy URL and date supporting retention/training labels.
- Whether the answer is limited to the named project, model, dates, or number of testers.
- Any condition that requires disabling the route.

Silence is not approval. A general marketing page is not an answer to this specific pooled-key
flow. If approval is denied or qualified beyond what the beta can satisfy, leave the route disabled
and use tester-owned keys instead.
