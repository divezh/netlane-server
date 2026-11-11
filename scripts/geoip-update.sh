#!/usr/bin/env bash
# Installs the monthly country database the panel uses to draw client flags.
#
# The database is DB-IP Lite Country by DB-IP (https://db-ip.com), licensed under
# Creative Commons Attribution 4.0. The panel reads the file locally and never
# calls an external service, so client addresses stay on the server.
#
#   geoip-update            install or refresh the current month
#   geoip-update 2026-11    install a named month
#
# Installed as /usr/local/sbin/geoip-update; the systemd timer runs it monthly.
set -euo pipefail
umask 022

target=${NETLANE_GEOIP_FILE:-/var/lib/trusttunnel-admin/geoip.mmdb}
month=${1:-$(date -u +%Y-%m)}
[[ $month =~ ^[0-9]{4}-[0-9]{2}$ ]] || { echo "geoip: month must look like 2026-11, got $month" >&2; exit 1; }
url="https://download.db-ip.com/free/dbip-country-lite-$month.mmdb.gz"

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

echo "geoip: downloading $url"
curl -fsSL --proto '=https' --max-time 300 -o "$work/db.mmdb.gz" "$url"
gzip -dc "$work/db.mmdb.gz" >"$work/geoip.mmdb"
[[ -s $work/geoip.mmdb ]] || { echo 'geoip: the downloaded database is empty' >&2; exit 1; }

# Replace atomically: the panel may be reading the file right now.
install -d -m 0755 "$(dirname -- "$target")"
install -m 0644 "$work/geoip.mmdb" "$target.new"
mv -f -- "$target.new" "$target"
chmod 0644 "$target"

echo "geoip: installed $target ($(du -h -- "$target" | cut -f1)), month $month"
