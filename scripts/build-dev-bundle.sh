#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
[[ -x artifacts/trusttunnel_endpoint ]] || { echo 'Build the managed Linux endpoint first.' >&2; exit 1; }
artifacts/trusttunnel_endpoint --version | grep -q -- '-admin-preview.5'
stage=$(mktemp -d)
trap 'rm -rf -- "$stage"' EXIT
mkdir -p "$stage/bin"
revision=$(git rev-parse --short HEAD)
# Release builds pass NETLANE_VERSION=X.Y.Z: the panel and netlane-update compare it with the latest release.
version=${NETLANE_VERSION:-dev-$revision}
[[ $version =~ ^([0-9]+\.[0-9]+\.[0-9]+|dev-[0-9a-f]+)$ ]] || { echo "bad NETLANE_VERSION: $version" >&2; exit 1; }
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$stage/bin/trusttunnel-admin" ./cmd/trusttunnel-admin
cp artifacts/trusttunnel_endpoint "$stage/bin/"
strip --strip-debug "$stage/bin/trusttunnel_endpoint"
cp deploy/dev/install.sh "$stage/install.sh"
cp deploy/dev/install-support.py "$stage/install-support.py"
# Interactive entry point for a fresh VPS: asks the questions, prepares the server, runs install.sh.
cp scripts/netlane-setup.sh "$stage/setup.sh"
# Updater for installed servers: netlane-update.
cp scripts/netlane-update.sh "$stage/update.sh"
# Country database refresher for client flags: geoip-update.
cp scripts/geoip-update.sh "$stage/geoip-update"
mkdir -p "$stage/backup" "$stage/fail2ban" "$stage/geoip"
cp deploy/backup/trusttunnel-backup deploy/backup/trusttunnel-backup.service deploy/backup/trusttunnel-backup.timer "$stage/backup/"
cp deploy/fail2ban/trusttunnel-admin.filter.conf deploy/fail2ban/trusttunnel-admin.jail.local "$stage/fail2ban/"
cp deploy/systemd/trusttunnel-geoip.service deploy/systemd/trusttunnel-geoip.timer "$stage/geoip/"
chmod 0755 "$stage/backup/trusttunnel-backup"
chmod 0644 "$stage/geoip/trusttunnel-geoip.service" "$stage/geoip/trusttunnel-geoip.timer"
# A Windows checkout may carry CRLF; the VPS needs LF in every shell, unit and fail2ban file.
sed -i 's/\r$//' "$stage/install.sh" "$stage/setup.sh" "$stage/update.sh" "$stage/geoip-update" "$stage/install-support.py" "$stage"/backup/* "$stage"/fail2ban/* "$stage"/geoip/*
chmod 0755 "$stage/install.sh" "$stage/setup.sh" "$stage/update.sh" "$stage/geoip-update" "$stage/bin/"*
git rev-parse HEAD >"$stage/SOURCE_COMMIT"
tar -czf artifacts/trusttunnel-dev.tar.gz -C "$stage" .
(cd artifacts && sha256sum trusttunnel-dev.tar.gz >trusttunnel-dev.tar.gz.sha256)
cat artifacts/trusttunnel-dev.tar.gz.sha256
