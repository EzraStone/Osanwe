# Conversation data lifecycle

## Default: ephemeral

A new installation starts in ephemeral mode. Messages exist in the page's memory only. Closing or
reloading the page loses them. Starting a new conversation removes the current transcript from the
page after any in-flight request is stopped.

Ephemeral does not mean invisible: the local process, relay/gateway path, model provider, operating
system, browser, and anything typed into a prompt still have the capabilities described in
[privacy boundaries](privacy-boundaries.md).

## Optional: save on this device

The person may explicitly enable device-only history. Saved conversations live in a versioned
IndexedDB database under the local Osanwë origin. Prompt text must never be copied into localStorage,
cookies, query strings, crash reports, or application logs.

The choice itself may be stored in localStorage because it contains no conversation content.

## Export

Export produces a versioned JSON file on the device. It contains the selected conversation and its
model identifier. It does not contain tokens, receipts, API keys, relay secrets, pins, payment
identifiers, or gateway credentials.

An exported file is plaintext. The download confirmation must say that anyone who can read the file
can read the conversation.

## Deletion

Delete conversation removes its IndexedDB record and clears rendered message nodes. Delete all
history clears the conversation store, not the entire browser profile. Both actions must stop an
in-flight request first so a late stream cannot recreate deleted content.

Deletion cannot recall text already sent to a model provider or remove copies made through an
export, screenshot, browser backup, operating-system snapshot, or compromised device.

## Failure behavior

If IndexedDB is unavailable, corrupt, blocked, or out of quota, the client falls back to ephemeral
mode and tells the person. It must not silently claim a conversation was saved.

Schema upgrades fail closed: unknown records are ignored until validated, never rendered as HTML,
and never sent to a provider automatically.
