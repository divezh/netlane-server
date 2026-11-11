#!/usr/bin/env bash
set -euo pipefail
version=${1:-0.1.0~dev}
[[ $version =~ ^[0-9][0-9A-Za-z.+~:-]*$ ]] || { echo 'Invalid package version' >&2; exit 1; }
root=$(cd "$(dirname "$0")/.." && pwd)
stage=$(mktemp -d)
trap 'rm -rf -- "$stage"' EXIT
mkdir -p "$stage/DEBIAN" "$stage/usr/bin" "$stage/usr/lib/systemd/system" "$root/artifacts"
cd "$root"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w -X main.version=$version" -o "$stage/usr/bin/trusttunnel-admin" ./cmd/trusttunnel-admin
cat >"$stage/DEBIAN/control" <<EOF
Package: trusttunnel-admin
Version: $version
Architecture: amd64
Maintainer: Netlane Console contributors
Depends: adduser, util-linux, ca-certificates
Section: admin
Priority: optional
Description: Netlane Console, private TrustTunnel VPN administration panel
 User definitions, policy storage and local metrics adapter.
 This preview does not yet apply VPN policies or install TrustTunnel.
EOF
cp deploy/systemd/trusttunnel-admin.service "$stage/usr/lib/systemd/system/"
cp deploy/debian/postinst "$stage/DEBIAN/postinst"
chmod 0755 "$stage/DEBIAN/postinst" "$stage/usr/bin/trusttunnel-admin"
dpkg-deb --root-owner-group --build "$stage" "$root/artifacts/trusttunnel-admin_${version}_amd64.deb"
sha256sum "$root/artifacts/trusttunnel-admin_${version}_amd64.deb"
