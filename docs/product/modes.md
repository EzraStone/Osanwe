# Product modes

The product uses four mode names, but only modes with complete behavior appear as active navigation.

## Chat

Chat is the first consumer mode. It supports text-only, multi-turn conversations, streaming answers,
model choice, runtime privacy labels, and ephemeral or explicitly local retention.

## Models

Models is the catalog and disclosure surface. It answers what is available, what the gateway accepts,
what each participant can learn, and which provider-policy facts remain unknown.

## Code

Code will apply the same accountless and local-first principles to repository work. Before it ships,
the project must define file access, command execution, secret redaction, patch review, and whether a
model may receive an entire repository or only selected context.

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

Code and Cowork remain roadmap language until those requirements are implemented. Empty tabs would
make the product look broader while making its promises less precise.
