# Private release-candidate procedure

The existing `release` GitHub Actions workflow already supports a non-tagged manual run. On
`workflow_dispatch`, it builds Windows, Linux, Intel macOS, and Apple Silicon macOS clients plus a
Windows installer. It uploads short-lived Actions artifacts but does **not** create a GitHub Release
or add download links to the public website. That is the private candidate path.

## Build

1. Push the reviewed commit to `main`.
2. In GitHub Actions, open **release** → **Run workflow** → choose `main`.
3. Require the preflight job, four archive jobs, installer build, and Windows smoke install to pass.
4. Download every `client-*` artifact while signed in. Record the workflow run URL and exact commit.
5. Compute SHA-256 locally for each downloaded artifact and store the results with the test record.

Do not tag a release for this step. Tags additionally create a draft GitHub Release, which is a
different publication surface and should remain behind the beta gates.

## Clean-machine validation

Test the actual downloaded bytes, not a local development build:

- Windows 11 standard user: install, first-run enrollment, relay-secret prompt, free-test invitation
  activation, restart persistence, one chat request, one code preview, uninstall, and remaining local
  data disclosure.
- macOS Apple Silicon and Intel where available: archive warning path, launcher, activation, restart,
  request, close, and removal.
- Ubuntu LTS: archive permissions, terminal launcher, activation, restart, request, close, removal.
- On every platform: wrong invitation, wrong mint key, expired epoch, exhausted daily allowance,
  provider 429, stopped relay, wrong relay pin, inactive model route, and expired route all fail
  visibly without fallback.

Record OS version, artifact digest, build identity from Settings, result, and issue link. Never put an
invitation, relay secret, provider key, prompt, or answer in the record.

## Publication gate

Only after provider approval, independent relay acceptance, gateway/mint migration, rollback and
expiry drills, and clean-platform validation may the operator:

1. create a signed version tag;
2. review the automatically created draft release and `SHA256SUMS`;
3. publish the release; and
4. replace website “not yet” copy with links to that exact release.

The website must never point straight at ephemeral Actions artifact URLs.
