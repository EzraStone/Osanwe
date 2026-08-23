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
if [ ! -t 0 ]; then
  printf '%s\n' "This launcher requires an interactive terminal so pasted secrets can be hidden." >&2
  printf 'Open a terminal in this folder and run: %s\n' "./$(basename -- "$0")" >&2
  exit 2
fi

restore_terminal() {
  stty echo 2>/dev/null || true
}
trap restore_terminal EXIT HUP INT TERM

hide_terminal_input() {
  if ! stty -echo 2>/dev/null; then
    printf '%s\n' "The launcher could not hide terminal input; no secret was read." >&2
    exit 2
  fi
}

show_terminal_input() {
  if ! stty echo 2>/dev/null; then
    printf '%s\n' "The launcher could not restore terminal echo; stop and reset this terminal before continuing." >&2
    exit 2
  fi
}

printf '%s' "Paste the relay secret (it will not be saved): "
hide_terminal_input
if ! IFS= read -r OSANWE_SECRET; then
  printf '\n%s\n' "No relay secret was read from the terminal." >&2
  exit 2
fi
show_terminal_input
printf '\n'
if [ -z "$OSANWE_SECRET" ]; then
  printf '%s\n' "The relay secret cannot be empty." >&2
  exit 2
fi
export OSANWE_SECRET

if grep -Eq '"mint"[[:space:]]*:[[:space:]]*"[^"[:space:]]' "$config_path"; then
  printf '%s' "Paste the beta entitlement (it will not be saved): "
  hide_terminal_input
  if ! IFS= read -r OSANWE_RECEIPT; then
    printf '\n%s\n' "No beta entitlement was read from the terminal." >&2
    exit 2
  fi
  show_terminal_input
  printf '\n'
  if [ -z "$OSANWE_RECEIPT" ]; then
    printf '%s\n' "The beta entitlement cannot be empty." >&2
    exit 2
  fi
  export OSANWE_RECEIPT
fi

exec "$binary_path" -config "$config_path" -open-ui
