#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
config_path="$script_dir/osanwe.json"
binary_path="$script_dir/bearer"

if [ ! -f "$config_path" ]; then
  printf '%s\n' "This Osanwe client has not been enrolled yet." >&2
  printf '%s\n' "Ask your beta inviter for osanwe.json and place it beside this launcher." >&2
  exit 2
fi
if [ ! -x "$binary_path" ]; then
  printf '%s\n' "The bearer program is missing or not executable. Download a complete release again." >&2
  exit 2
fi

restore_terminal() {
  stty echo 2>/dev/null || true
}
trap restore_terminal EXIT HUP INT TERM

printf '%s' "Paste the relay secret (it will not be saved): "
stty -echo
IFS= read -r OSANWE_SECRET
stty echo
printf '\n'
if [ -z "$OSANWE_SECRET" ]; then
  printf '%s\n' "The relay secret cannot be empty." >&2
  exit 2
fi
export OSANWE_SECRET

if grep -Eq '"mint"[[:space:]]*:[[:space:]]*"[^"[:space:]]' "$config_path"; then
  printf '%s' "Paste the beta entitlement (it will not be saved): "
  stty -echo
  IFS= read -r OSANWE_RECEIPT
  stty echo
  printf '\n'
  if [ -z "$OSANWE_RECEIPT" ]; then
    printf '%s\n' "The beta entitlement cannot be empty." >&2
    exit 2
  fi
  export OSANWE_RECEIPT
fi

exec "$binary_path" -config "$config_path" -open-ui
