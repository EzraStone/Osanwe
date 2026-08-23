# Model labels

Model labels state observable facts. They do not produce a privacy score.

## Availability

- **Available now:** reported by the connected gateway's live catalog.
- **Unavailable:** known to the interface but absent from the live catalog.
- **Unknown:** the catalog could not be read; the interface must not guess.

## Request capabilities

The first catalog may state:

- text input and output only;
- streaming support;
- maximum output tokens enforced by this gateway; and
- the request API style used by the local client.

Image, audio, file, remote-URL, tool-use, browsing, and prompt-cache support are false unless the
gateway explicitly validates, prices, and reports them.

## Network identity

- **Relay verified:** the local client established a tunnel to the pinned or directory-published
  relay key.
- **Provider account unlinked:** token mode uses a gateway credential rather than the person's
  provider key.
- **Provider account linked:** bring-your-own-key mode hides the network address but still uses the
  person's provider account.

These labels never claim that prompt content or writing style is anonymous.

## Content access

- The relay cannot read prompt or answer text.
- The gateway can currently read both.
- The model provider receives both.
- Attested execution is not available until the client verifies a measurement and reports that fact.

## Retention

Osanwë reports only its own retention behavior from runtime state:

- ephemeral in this page;
- saved on this device; or
- no server-side conversation record.

Provider retention or training behavior must be labeled **unknown** unless model metadata includes a
dated source. Product copy must not translate “API” into “not trained” or “zero retention.”

## Price

Do not infer retail price from gateway cost rates. One anonymous credential may include provider
cost, relay operation, payment fees, abuse reserves, sponsorship, or a subsidy. The purchase surface
must state exactly what one entitlement buys before payment.
