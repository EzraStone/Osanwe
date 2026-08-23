# Start Osanwë

This archive opens a local browser app. It does not host your chat on the public website.

## Invited beta tester

1. Put the `osanwe.json` supplied by your inviter beside the programs in this folder.
2. Put `gateway.crt` beside it if the config names that file.
3. Windows: double-click **Start Osanwe.cmd**.
4. macOS: double-click **Start Osanwe.command**. Linux: open **start-osanwe.sh** from your file manager.
5. Paste the relay secret and, in token mode, the beta entitlement when asked. They remain in this
   process only and are not written to the config file or passed as command-line arguments.

The launcher starts a loopback-only client and opens `http://127.0.0.1:8080/_osanwe/`. Leave the
launcher window open while using Osanwë. Closing it stops the local client.

## Before sending a prompt

- Confirm the Connect view names the relay you were given.
- A manually configured pin is shown as configured; only a directory-selected relay with an
  observed connection is shown as matched.
- Read the model's provider retention, training, identity, source date, and lifecycle labels.
- Use only the supplied synthetic or deliberately non-sensitive test prompts.

This beta is free, experimental, text-only, and not appropriate for medical, legal, financial,
employment, school, children's, or vulnerable-source data. The gateway can currently read prompt
and answer text. Attested execution is not built.

## Removal

Stop the launcher and delete this folder. If you selected device history in the local app, use
Settings → Delete all saved history before deleting the folder; that history belongs to the browser
profile, not this archive.
