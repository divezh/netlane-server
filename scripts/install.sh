#!/usr/bin/env bash
# Development installer. A complete VPN release will replace this preview flow.
set -euo pipefail

usage() { printf 'Usage: %s --check | --preview --package /path/to/trusttunnel-admin.deb\n' "$0"; }
check=false
preview=false
package=''
while (($#)); do
  case "$1" in
    --check) check=true; shift ;;
    --preview) preview=true; shift ;;
    --package) (($# >= 2)) || { usage; exit 2; }; package=$2; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) usage; exit 2 ;;
  esac
done

# This override exists only for non-mutating OS-matrix tests.
os_release=/etc/os-release
if $check && [[ -n ${TT_TEST_OS_RELEASE:-} ]]; then os_release=$TT_TEST_OS_RELEASE; fi
[[ -r $os_release ]] || { echo 'Cannot read OS information.' >&2; exit 1; }
# Parse only the two required keys. Never execute /etc/os-release as shell code.
os_id=$(sed -n 's/^ID=//p' "$os_release" | tr -d '"')
os_version=$(sed -n 's/^VERSION_ID=//p' "$os_release" | tr -d '"')
case "$os_id:$os_version" in
  ubuntu:24.04|ubuntu:26.04|debian:13) ;;
  *) printf 'Unsupported OS: %s %s. Expected Ubuntu 24.04/26.04 or Debian 13.\n' "$os_id" "$os_version" >&2; exit 1 ;;
esac
arch=$(dpkg --print-architecture)
[[ $arch == amd64 ]] || { printf 'Unsupported architecture: %s (first preview supports amd64).\n' "$arch" >&2; exit 1; }
printf 'OS supported: %s %s (%s)\n' "$os_id" "$os_version" "$arch"
if $check; then echo 'Preflight passed. No system changes were made.'; exit 0; fi
((EUID == 0)) || { echo 'Run the installer with sudo.' >&2; exit 1; }
$preview || { echo 'Only an API preview exists. Pass --preview explicitly; this does not install a complete VPN.' >&2; exit 1; }
[[ -n $package && -f $package ]] || { echo 'Specify a local .deb with --package.' >&2; exit 1; }
command -v systemctl >/dev/null && [[ -d /run/systemd/system ]] || { echo 'A running systemd host is required.' >&2; exit 1; }
[[ $(dpkg-deb -f "$package" Package) == trusttunnel-admin ]] || { echo 'Unexpected package name.' >&2; exit 1; }
[[ $(dpkg-deb -f "$package" Architecture) == "$arch" ]] || { echo 'Package architecture mismatch.' >&2; exit 1; }

dpkg -i -- "$package"
if [[ ! -f /etc/trusttunnel-admin/config.json ]]; then
  [[ -r /dev/tty && -w /dev/tty ]] || { echo 'Open a terminal to complete the setup wizard.' >&2; exit 1; }
  runuser -u trusttunnel-admin -- /usr/bin/trusttunnel-admin setup </dev/tty >/dev/tty
fi
systemctl daemon-reload
systemctl enable --now trusttunnel-admin
sleep 1
systemctl is-active --quiet trusttunnel-admin || { journalctl -u trusttunnel-admin -n 15 --no-pager; exit 1; }
echo 'API preview installed at http://127.0.0.1:8080. VPN integration and the working UI are not yet available.'
echo 'Existing TrustTunnel configuration and network rules were not changed.'
