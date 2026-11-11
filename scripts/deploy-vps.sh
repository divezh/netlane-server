#!/usr/bin/env bash
# Fast Netlane install on a fresh VPS, run from your own Linux/macOS/WSL machine.
#   scripts/deploy-vps.sh root@203.0.113.10 vpn.example.com
# Without --bundle the latest release is downloaded with the GitHub CLI
# (gh auth login once).
set -euo pipefail

repo=divezh/netlane-server
usage() {
  cat <<'EOF'
Usage: deploy-vps.sh USER@HOST DOMAIN [options]
  --bundle PATH      use a local trusttunnel-dev.tar.gz (its .sha256 must be next to it)
  --no-firewall      do not enable UFW on the VPS
  --no-key           do not add your SSH public key to the VPS
  --key PATH         public key to add (default: ~/.ssh/id_ed25519.pub, then id_rsa.pub)
  --skip-dns-check   do not require DOMAIN to resolve to HOST
EOF
}
(($# >= 2)) || { usage; exit 2; }
target=$1
domain=$(printf '%s' "$2" | tr '[:upper:]' '[:lower:]')
shift 2
bundle='' firewall=1 add_key=1 key='' dns_check=1
while (($#)); do
  case $1 in
    --bundle) bundle=${2:?--bundle needs a path}; shift 2 ;;
    --no-firewall) firewall=0; shift ;;
    --no-key) add_key=0; shift ;;
    --key) key=${2:?--key needs a path}; shift 2 ;;
    --skip-dns-check) dns_check=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage; exit 2 ;;
  esac
done
[[ $target == *@* ]] || { echo 'Target must look like root@203.0.113.10.' >&2; exit 2; }
[[ $domain =~ ^[a-z0-9][a-z0-9.-]*\.[a-z]{2,}$ ]] || { echo "Invalid domain: $domain" >&2; exit 2; }
for tool in ssh scp; do command -v "$tool" >/dev/null || { echo "$tool is required." >&2; exit 1; }; done
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

echo '1/4 Install bundle'
if [[ -n $bundle ]]; then
  [[ -f $bundle && -f $bundle.sha256 ]] || { echo "Need $bundle and $bundle.sha256." >&2; exit 1; }
  cp -- "$bundle" "$work/trusttunnel-dev.tar.gz"
  cp -- "$bundle.sha256" "$work/trusttunnel-dev.tar.gz.sha256"
else
  command -v gh >/dev/null || { echo 'Install the GitHub CLI (gh) and run "gh auth login", or pass --bundle.' >&2; exit 1; }
  tag=$(gh release view -R "$repo" --json tagName -q .tagName) || { echo 'No release was found (is gh logged in?).' >&2; exit 1; }
  echo "    release $tag"
  gh release download "$tag" -R "$repo" -p 'trusttunnel-dev.tar.gz' -p 'trusttunnel-dev.tar.gz.sha256' -D "$work"
fi
(
  cd "$work"
  if command -v sha256sum >/dev/null; then sha256sum -c --quiet trusttunnel-dev.tar.gz.sha256
  else shasum -a 256 -c --quiet trusttunnel-dev.tar.gz.sha256; fi
)
echo '    checksum OK'

echo '2/4 DNS'
host=${target#*@}
resolve() {
  if command -v getent >/dev/null; then getent ahostsv4 "$1" | awk '{print $1}' | sort -u
  else dig +short A "$1" 2>/dev/null; fi
}
if [[ $host =~ ^[0-9.]+$ ]]; then host_ips=$host; else host_ips=$(resolve "$host"); fi
domain_ips=$(resolve "$domain" || true)
if [[ -z $domain_ips ]] || ! grep -qxF -f <(printf '%s\n' "$host_ips") <(printf '%s\n' "$domain_ips"); then
  if ((dns_check)); then
    echo "    $domain -> $(echo "${domain_ips:-nothing}" | xargs), VPS -> $(echo "$host_ips" | xargs)" >&2
    echo '    Create an A record for the domain pointing to the VPS (Cloudflare: DNS only), wait, retry.' >&2
    echo '    Use --skip-dns-check only if your resolver is wrong.' >&2
    exit 1
  fi
  echo '    DNS check skipped'
else
  echo "    $domain -> $(echo "$domain_ips" | xargs)"
fi

uploads=("$work/trusttunnel-dev.tar.gz" "$work/trusttunnel-dev.tar.gz.sha256" "$here/netlane-remote-install.sh")
if ((add_key)); then
  if [[ -z $key ]]; then
    for candidate in ~/.ssh/id_ed25519.pub ~/.ssh/id_rsa.pub; do [[ -f $candidate ]] && { key=$candidate; break; }; done
  fi
  if [[ -n $key && -f $key ]]; then
    cp -- "$key" "$work/netlane-authorized-key.pub"
    uploads+=("$work/netlane-authorized-key.pub")
  else
    echo '    No SSH public key found; create one with ssh-keygen -t ed25519 to stop using passwords.'
  fi
fi

echo "3/4 Upload to $target (SSH may ask for the password)"
scp -q -- "${uploads[@]}" "$target:"

echo '4/4 Install (2–4 minutes)'
args=$domain
((firewall)) || args="$args --no-firewall"
# shellcheck disable=SC2029 # $args is expanded locally on purpose: domain and flag only.
ssh -t "$target" "if [ \$(id -u) = 0 ]; then bash ~/netlane-remote-install.sh $args; else sudo bash ~/netlane-remote-install.sh $args; fi"
