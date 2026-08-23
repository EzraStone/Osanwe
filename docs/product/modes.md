# Product modes

The primary navigation uses three mode names. Chat and the bounded Code beta are active; Cowork is
visible but disabled so the roadmap is clear without suggesting unfinished behavior exists.

## Chat

Chat is the first consumer mode. It supports text-only, multi-turn conversations, streaming answers,
model choice, runtime privacy labels, and ephemeral or explicitly local retention.

## Code

Code is an early text-only coding assistant. It gives the provider a code-focused system instruction
and keeps its conversation separate from Chat. It can explain, generate, debug, and review text the
person supplies. It cannot read local files, run commands, or apply patches, and the interface says so.

Repository access remains later work. Before that ships, the project must define file permission,
command execution, secret redaction, patch review, and whether a model may receive an entire
repository or only selected context.

## Cowork

Cowork will support longer asynchronous tasks and artifact production. It needs explicit task
lifetimes, local artifact storage, resumability without a cloud identity, and visible boundaries for
connectors that identify a person.

## Shared requirements

Every mode must:

- work without a conventional Osanwë account;
- state when an external provider or connector identifies the person;
- keep its local data lifecycle visible and controllable;
- avoid analytics and prompt-derived profiling;
- expose no new paid endpoint until its request cost is bounded; and
- reuse the same model catalog and factual privacy labels.

Model catalog and connection diagnostics live under Settings instead of competing with product modes
in the primary navigation. Cowork remains roadmap language until its requirements are implemented.
