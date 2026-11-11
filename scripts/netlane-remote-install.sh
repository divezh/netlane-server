#!/usr/bin/env bash
# Runs on the new VPS. Uploaded to the SSH user's home together with the install bundle
# by scripts/deploy-vps.ps1 or scripts/deploy-vps.sh; not meant to be started by hand.
# Usage: netlane-remote-install.sh DOMAIN [--no-firewall]
set -euo pipefail
umask 077
domain=${1:-}
firewall=1
[[ ${2:-} == --no-firewall ]] && firewall=0
[[ -n $domain ]] || { echo 'Usage: netlane-remote-install.sh DOMAIN [--no-firewall]' >&2; exit 2; }
((EUID == 0)) || { echo 'Root is required (the deploy script uses sudo for non-root users).' >&2; exit 1; }
dir=$(cd "$(dirname "$0")" && pwd)
cd "$dir"
cleanup_uploads() { rm -f -- "$dir/trusttunnel-dev.tar.gz" "$dir/trusttunnel-dev.tar.gz.sha256" "$dir/netlane-authorized-key.pub" "$dir/netlane-remote-install.sh"; }
[[ -f trusttunnel-dev.tar.gz && -f trusttunnel-dev.tar.gz.sha256 ]] || { echo 'Install bundle was not uploaded.' >&2; exit 1; }
sha256sum -c --quiet trusttunnel-dev.tar.gz.sha256
work=$(mktemp -d /root/trusttunnel-install.XXXXXX)
trap 'rm -rf -- "$work"' EXIT
tar -xzf trusttunnel-dev.tar.gz -C "$work"

if [[ -s netlane-authorized-key.pub ]]; then
  key=$(head -n 1 netlane-authorized-key.pub)
  if [[ $key =~ ^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh.com)\ [A-Za-z0-9+/=]+(\ .*)?$ ]]; then
    owner=${SUDO_USER:-root}
    home=$(getent passwd "$owner" | cut -d: -f6)
    install -d -m 0700 -o "$owner" -g "$(id -gn "$owner")" "$home/.ssh"
    touch "$home/.ssh/authorized_keys"
    chown "$owner:$(id -gn "$owner")" "$home/.ssh/authorized_keys"
    chmod 0600 "$home/.ssh/authorized_keys"
    if grep -qxF -- "$key" "$home/.ssh/authorized_keys"; then
      echo "SSH key already present for $owner."
    else
      printf '%s\n' "$key" >>"$home/.ssh/authorized_keys"
      echo "SSH key added for $owner."
    fi
  else
    echo 'Skipping SSH key: the uploaded file is not a public key.' >&2
  fi
fi

# 144 random bits as hex; shown once at the end, changeable in the panel.
password=$(od -An -N18 -tx1 /dev/urandom | tr -d ' \n')
printf '%s\n' "$password" | TT_PASSWORD_STDIN=1 TT_FIREWALL=$firewall bash "$work/install.sh" "$domain"
cleanup_uploads
cat <<EOF

================ Netlane установлен ================
Панель:  https://$domain:8443/
Логин:   admin
Пароль:  $password

Сохраните пароль сейчас: он показан один раз и больше нигде не хранится
в открытом виде. Сменить его можно кнопкой «Пароль» в панели.
Резервные копии: /var/backups/trusttunnel (ежедневно в 04:00 MSK).
===========================================================
EOF
