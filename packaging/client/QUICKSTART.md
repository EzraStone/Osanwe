# Start Osanwë

This archive opens a local browser app. It does not host your chat on the public website.

## Invited beta tester

1. Put the `osanwe.json` supplied by your inviter beside the programs in this folder.
2. Put `gateway.crt` beside it if the config names that file.
3. Windows: double-click **Start Osanwe.cmd**.
4. macOS: double-click **Start Osanwe.command**. Linux: open a terminal in this folder and run
   `./start-osanwe.sh`; the launcher requires an interactive terminal so it can hide pasted secrets.
5. Paste the relay secret and, in token mode, the beta entitlement when asked. They are passed to the
   client through its process environment, immediately removed from the client's child environment,
   and are not written to the config file or placed on its command line.

The launcher starts a loopback-only client and opens `http://127.0.0.1:8080/_osanwe/`. Leave the
launcher window open while using Osanwë. Closing it stops the local client.

## Before sending a prompt

- Confirm the Connect view names the relay you were given.
- A manually configured pin is shown as configured; only a directory-selected relay with an
  observed connection is shown as matched.
- Read the model's provider retention, training, identity, source date, and lifecycle labels.
- Acknowledge that the configured model provider receives both prompt and answer text under the
  gateway account, subject to that provider's own retention and training policy.
- Use only the supplied synthetic or deliberately non-sensitive test prompts.

This beta is free, experimental, text-only, and not appropriate for medical, legal, financial,
employment, school, children's, or vulnerable-source data. The gateway can currently read prompt
and answer text. Attested execution is not built. A separately operated relay is an experimental
beta condition, and the client cannot prove that the relay and gateway operators are independent.

## Removal

Stop the launcher and delete this folder. If you selected device history in the local app, use
Settings → Delete all saved history before deleting the folder; that history belongs to the browser
profile, not this archive.
