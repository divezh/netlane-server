#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
fixture=$(mktemp)
trap 'rm -f -- "$fixture"' EXIT
for pair in ubuntu:24.04 ubuntu:26.04 debian:13; do
  printf 'ID=%s\nVERSION_ID="%s"\n' "${pair%:*}" "${pair#*:}" >"$fixture"
  TT_TEST_OS_RELEASE=$fixture bash "$root/scripts/install.sh" --check
done
printf 'ID=debian\nVERSION_ID="12"\n' >"$fixture"
if TT_TEST_OS_RELEASE=$fixture bash "$root/scripts/install.sh" --check; then echo 'Unsupported OS accepted' >&2; exit 1; fi
echo 'Installer OS guards passed.'
