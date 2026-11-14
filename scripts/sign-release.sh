#!/usr/bin/env bash
# Signs a draft release with the owner's key and publishes it (docs/SIGNING.md).
#   scripts/sign-release.sh divezh/netlane-server netlane-0.6.1 trusttunnel-dev.tar.gz ~/netlane-release.key
# Needs gh (logged in) and openssl. The key never leaves this computer: only the .sig is uploaded.
set -euo pipefail
repo=${1:?repository}; tag=${2:?tag}; asset=${3:?asset name}; key=${4:?private key}
work=$(mktemp -d); trap 'rm -rf -- "$work"' EXIT
[[ $(gh release view "$tag" -R "$repo" --json isDraft --jq .isDraft) == true ]] || { echo "$tag is not a draft: published releases are immutable" >&2; exit 1; }
gh release download "$tag" -R "$repo" -p "$asset" -p "$asset.sha256" -D "$work"
(cd "$work" && sha256sum -c "$asset.sha256")
openssl dgst -sha256 -sign "$key" -out "$work/$asset.sig" "$work/$asset"
openssl pkey -in "$key" -pubout -out "$work/key.pub"
openssl dgst -sha256 -verify "$work/key.pub" -signature "$work/$asset.sig" "$work/$asset"
gh release upload "$tag" "$work/$asset.sig" -R "$repo" --clobber
gh release edit "$tag" -R "$repo" --draft=false --latest
echo "Published $repo $tag with $asset.sig"
