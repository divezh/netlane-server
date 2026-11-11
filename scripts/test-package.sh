#!/usr/bin/env bash
# Run only in a disposable container: this installs the preview package.
set -euo pipefail
[[ ${TT_PACKAGE_TEST:-} == 1 ]] || { echo 'Refusing to run outside an explicit disposable package test.' >&2; exit 1; }
root=$(cd "$(dirname "$0")/.." && pwd)
bash -n "$root/scripts/install.sh" "$root/scripts/build-deb.sh"
bash "$root/scripts/install.sh" --check
apt-get update -qq
apt-get install -y -qq adduser util-linux ca-certificates >/dev/null
dpkg -i "$root/artifacts/trusttunnel-admin_0.1.0~dev_amd64.deb"
trusttunnel-admin version
[[ $(stat -c %a /etc/trusttunnel-admin) == 700 ]]
[[ $(stat -c %U /etc/trusttunnel-admin) == trusttunnel-admin ]]
[[ $(stat -c %a /var/lib/trusttunnel-admin) == 700 ]]
[[ ! -f /etc/trusttunnel-admin/config.json ]]
# A second install must be safe and preserve the service account.
dpkg -i "$root/artifacts/trusttunnel-admin_0.1.0~dev_amd64.deb"
echo 'Package installed twice; directories are private; no unconfigured service was started.'
