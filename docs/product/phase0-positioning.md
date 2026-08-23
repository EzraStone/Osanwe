# How Phase 0 informs the product

Phase 0 measures the latency added by Osanwë's network path. It does not require a production mint,
payment adapter, or migrated gateway, so it runs in parallel with security and product work.

## Current evidence

The first recorded route completed 60 direct and 60 relay requests. Its p95 direct latency was
273 ms, p95 relay latency was 722 ms, and measured overhead was 448 ms. Under the current threshold,
that route is a provisional failure for interactive chat.

One route is not a campaign. The result must not be generalized to all regions, providers, relays,
or workloads, and it must not be hidden because it is inconvenient.

## Decisions while the campaign continues

- Do not promise latency or market Osanwë as faster than direct provider access.
- Build shared accountless foundations that also serve asynchronous work: model discovery, privacy
  labels, true conversation state, export, deletion, and bounded request APIs.
- Keep text-only restrictions visible. If the eventual position emphasizes agents and Cowork,
  attachments and tool execution become product blockers that require new validation and pricing.
- Record region, route, provider, sample count, and threshold with every verdict.
- Do not move payment or deployment work ahead of measurement simply to consume cloud credit.

## Possible outcomes

- **Interactive passes:** continue Chat as the lead experience and optimize tail latency.
- **Mixed:** describe supported region/provider pairs and keep asynchronous modes prominent.
- **Interactive fails:** lead with Code, Cowork, batch, and agentic work where privacy is worth added
  seconds; Chat remains available without being the product's performance claim.

None of those outcomes changes the dignity mission. They change which workflow makes the privacy
tradeoff worthwhile.
