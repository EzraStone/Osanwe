# ADR 0004 — Keep the hosted BYOK preview separate from relay privacy claims

- **Status:** Accepted
- **Decides:** the role of the Vercel application
- **Supersedes:** the blanket prohibition on any hosted chat in the first beta charter
- **Revisit when:** a hosting platform can demonstrate the relay path's operator-separation property

## Context

A browser-only trial is materially easier to share than a downloaded local client. It also moves the
visitor's provider credential and prompt into a server function operated through the hosting
platform. That runtime is a conventional proxy: it may avoid intentional application persistence,
but it does not provide Osanwë's separation between source address, provider identity, and content.

Removing the hosted path would preserve a simpler story at the cost of making interface and provider
testing harder. Calling it the privacy product would preserve convenience at the cost of making a
false claim.

## Decision

Maintain the hosted application as an explicitly labeled **hosted BYOK preview**. Its purpose is to
test the interface, provider compatibility, model behavior, streaming, and the local code sandbox.
Maintain the downloadable loopback client and independently operated relay as the only candidate
path for the relay privacy claims.

The hosted preview must:

- disclose before key entry that the host processes the key, prompt, and answer in plaintext;
- keep keys in browser memory rather than storage and forward them only to fixed provider endpoints;
- avoid accounts, conversation databases, prompt analytics, and provider-error reflection;
- keep the public promotional site independent from the chat application;
- link to the relay-client boundary; and
- describe itself as compatibility infrastructure, never as anonymous or operator-separated access.

## Consequences

The project now has two intentionally different trust boundaries sharing one interface. Documentation,
testing, support, and beta reports must name which path produced an observation. Hosted-preview
latency is not Phase 0 relay latency. A successful hosted request is not evidence that a relay is
blind, independent, correctly pinned, or even running.

This choice makes early evaluation easier without weakening the technical claim, provided the labels
remain visible and the two paths are never combined into one unqualified privacy statement.
