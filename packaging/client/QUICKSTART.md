# Open Osanwe

Osanwe runs as a local app. The public website never receives the relay secret, beta entitlement,
provider key, prompt, answer, or local history.

## Windows: install once, reopen normally

1. Download `Osanwe-Setup_<version>_windows_amd64.exe` and compare its SHA-256 digest with the
   release's `SHA256SUMS`.
2. Run the installer. It installs only for your Windows user, creates Start Menu and optional
   desktop shortcuts, and does not require administrator access.
3. Open **Osanwe**. The first launch asks you to choose the `osanwe.json` supplied by the beta
   inviter. If that file names a relative gateway certificate, keep the certificate beside the
   enrollment file for this first import.
4. Enter the relay secret and, in token mode, the beta entitlement in the masked native prompt.
   They are held only for that app session and are not written to disk or put on a command line.
5. Osanwe opens in a dedicated local app window. Put a bring-your-own provider key only in
   **Settings → Models and connection**.
6. Close the app window to stop the local client. Reopen it later from Start or the desktop shortcut.

The installer and application are not yet code-signed, so Windows SmartScreen may warn. Do not
bypass a warning unless the digest matches the release and the invite came through the expected
channel.

Use **Change Osanwe enrollment** in the Start Menu if the beta operator gives you a replacement
`osanwe.json`.

## Windows portable archive

The `.zip` remains available for testers who do not want an installed app. Extract it and
double-click **Start Osanwe.cmd**. It uses the same first-run enrollment picker and local app window;
its programs remain in the extracted folder.

## macOS and Linux archive

1. Put the `osanwe.json` supplied by your inviter beside the programs in the extracted folder.
2. Put `gateway.crt` beside it if the config names that file.
3. On macOS, double-click **Start Osanwe.command**. On Linux, open a terminal in the folder and run
   `./start-osanwe.sh`; secret entry requires an interactive terminal so pasted values can be hidden.
4. Paste the relay secret and, in token mode, the beta entitlement when asked. They are passed to the
   client through its process environment, removed before browser helpers start, and are not saved in
   the config file or placed on the command line.

## Before sending a prompt

- Confirm **Settings → Models and connection** names the expected relay and provider route.
- Read the model's provider retention, training, identity, source-date, and lifecycle labels.
- Acknowledge that the configured model provider receives prompt and answer text under the account
  described by the runtime disclosure.
- Use only supplied synthetic or deliberately non-sensitive test prompts.

This beta is free, experimental, text-only, and not appropriate for medical, legal, financial,
employment, school, children's, or vulnerable-source data. The gateway can currently read prompt
and answer text. Attested execution is not built. A separately operated relay is an experimental
beta condition, and the client cannot prove operator independence.

## Removal

On Windows, use **Uninstall Osanwe** from Start or Windows Installed apps. The uninstaller removes
the app and shortcuts but intentionally leaves `%LOCALAPPDATA%\Osanwe`, because that folder can hold
the enrollment and browser-only history. Delete saved history from Settings first, then delete that
folder if you want to remove all local Osanwe data.

On macOS or Linux, stop the launcher and delete the extracted folder. If you enabled device history,
use Settings → Delete all saved history before removal; that history belongs to the browser profile,
not the archive.
