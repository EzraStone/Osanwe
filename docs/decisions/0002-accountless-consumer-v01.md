# ADR 0002 — Build an accountless local consumer client

- **Status:** Accepted
- **Decides:** the first consumer-facing product slice
- **Supersedes:** nothing
- **Revisit when:** Phase 0 is complete or a hosted client can preserve the same boundaries

## Context

The repository has strong transport, token, budget, and routing primitives, but the browser page is
still an operator demo. It looks like a chat while sending only the newest turn, has no Models view,
and offers no explicit local-retention choice.

Phase 0 currently has one provisional failing route for interactive latency. That result is enough
to avoid a framework migration or a hosted account system, but not enough to stop improving the
shared client foundation needed by chat, code, and asynchronous work.

## Decision

Osanwë v0.1 will be an accountless web client served by the local bearer process. It will remain
same-origin, embedded in the Go binary, dependency-free, and usable without analytics, a CDN, or a
cloud account.

The first slice provides:

- genuine multi-turn Chat;
- a live Models view;
- factual privacy labels derived from runtime state;
- ephemeral conversations by default;
- optional, device-only conversation storage with export and deletion; and
- the existing Connect view for developer tools.

Code and Cowork are named future modes, not incomplete tabs. Payment checkout stays outside the
prompt origin until its privacy boundary has been reviewed.

## Consequences

- The local client stays useful whether Phase 0 ultimately positions Osanwë for interactive or
  asynchronous work.
- Third-party script compromise is avoided because no frontend package or remote asset is added.
- Conversation history creates no server record. Opting into local history still creates a
  shared-device risk, which the interface must say plainly.
- No UI copy may imply attested execution, universal provider support, or finished latency work.
- A later SvelteKit or other frontend build must preserve the same-origin, no-remote-code, and
  accountless properties to replace this decision.
