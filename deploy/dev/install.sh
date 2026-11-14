#!/usr/bin/env bash
# Netlane installation on a fresh host or explicit recovery of an incomplete install.
set -Eeuo pipefail
umask 077
export DEBIAN_FRONTEND=noninteractive
domain=${1:-}
[[ $domain =~ ^[a-z0-9][a-z0-9.-]*\.[a-z]{2,}$ ]] || { echo 'Supply an ASCII domain name.' >&2; exit 2; }
((EUID == 0)) || { echo 'Root is required.' >&2; exit 1; }
# TT_ROLE=node turns this server into a node of a main server: TT_JOIN is the link from the
# panel («Ноды → Добавить ноду»). A node has no web panel and opens no 8443.
role=${TT_ROLE:-main}
case $role in
  main) ;;
  node) [[ ( ${TT_RESUME:-0} == 1 && -f /etc/trusttunnel-admin/config.json ) || ${TT_JOIN:-} =~ ^https://[^/]+/[a-z2-7]{26}/node/join#[A-Za-z0-9_-]+$ ]] || { echo 'TT_JOIN must be the node link from the panel: https://<main>/<prefix>/node/join#<code>.' >&2; exit 2; } ;;
  *) echo 'TT_ROLE must be main or node.' >&2; exit 2 ;;
esac
root=$(cd "$(dirname "$0")" && pwd)
[[ -x $root/bin/trusttunnel-admin && -x $root/bin/trusttunnel_endpoint && -x $root/backup/trusttunnel-backup && -f $root/fail2ban/trusttunnel-admin.filter.conf ]] || { echo 'Incomplete deployment bundle.' >&2; exit 1; }
[[ -f $root/install-support.py ]] || { echo "Bundle lacks install-support.py" >&2; exit 1; }
[[ $(uname -m) == x86_64 ]] || { echo 'This bundle requires x86_64.' >&2; exit 1; }
[[ -d /run/systemd/system ]] || { echo 'A running systemd host is required.' >&2; exit 1; }
os_id=$(sed -n 's/^ID=//p' /etc/os-release | tr -d '"')
os_version=$(sed -n 's/^VERSION_ID=//p' /etc/os-release | tr -d '"')
case "$os_id:$os_version" in ubuntu:24.04|ubuntu:26.04|debian:13) ;; *) echo 'Unsupported OS.' >&2; exit 1 ;; esac
# Refuse completed/production installs before changing packages or firewall rules.
state_file=/var/lib/netlane-install/state.json
[[ -f $state_file ]] || state_file=/var/lib/wtt-install/state.json
if [[ -f $state_file ]] && command -v python3 >/dev/null; then
  python3 -c 'import json,sys; s=json.load(open(sys.argv[1])); assert not s.get("complete"), "Installation complete: use netlane-update"' "$state_file"
fi
if ! command -v python3 >/dev/null; then
  for existing in /etc/trusttunnel/vpn.toml /etc/trusttunnel-admin/config.json /var/lib/trusttunnel-admin/panel.sqlite; do
    [[ ! -e $existing ]] || { echo 'Existing installation without Python: review manually.' >&2; exit 1; }
  done
  apt-get update -qq </dev/null
  apt-get install -y -qq python3 </dev/null
fi
# util-linux/flock are part of the supported base systems. Lock the whole installer.
exec 9>/run/lock/netlane-install.lock
flock -n 9 || { echo 'Another installer is running.' >&2; exit 1; }
[[ ${TT_PANEL_PUBLIC:-0} == 0 || ${TT_PANEL_PUBLIC:-0} == 1 ]] || { echo 'TT_PANEL_PUBLIC must be 0 or 1.' >&2; exit 2; }
python3 -c 'import ipaddress,os
raw=os.environ.get("TT_PANEL_ALLOW", "")
if raw:
    for item in raw.split(","): ipaddress.ip_network(item.strip(), strict=False)'
panel_listen=127.0.0.1:8443
if [[ ${TT_PANEL_PUBLIC:-0} == 1 || -n ${TT_PANEL_ALLOW:-} ]]; then panel_listen=0.0.0.0:8443; fi
panel_listen=$(python3 "$root/install-support.py" begin "$domain" "$role" "$panel_listen")
diagnose_failure() {
  local status=$?
  printf 'Installer failed: line=%s exit=%s (command text omitted to protect secrets).\n' "${1:-unknown}" "$status" >&2
  echo 'Installation interrupted. Configs/data preserved. Re-run setup.sh --resume with the same domain/role.' >&2
  systemctl --no-pager --full status trusttunnel-managed "${service:-trusttunnel-admin}" fail2ban >&2 || true
  namei -l /opt/trusttunnel/bin/trusttunnel_endpoint /opt/trusttunnel-admin/bin/trusttunnel-admin >&2 || true
  exit "$status"
}
trap 'diagnose_failure "$LINENO"' ERR
# Ubuntu 26.04's uutils test can reject access granted by supplementary groups.
# Check permissions through the OS, as the actual service identity, instead.
require_access() {
  local account=$1 access=$2 path=$3
  runuser -u "$account" -- python3 -c '
import os, stat, sys
access, path = sys.argv[1:]
flags = sum({"r": os.R_OK, "w": os.W_OK, "x": os.X_OK}.get(c, 0) for c in access)
valid = os.access(path, flags)
if "s" in access:
    valid = valid and stat.S_ISSOCK(os.stat(path).st_mode)
if not valid:
    raise SystemExit(f"Required access {access} denied: {path} (uid={os.getuid()})")
' "$access" "$path"
}
if [[ -n ${TT_PANEL_ALLOW:-} && ${TT_FIREWALL:-0} != 1 ]]; then
  if ! command -v ufw >/dev/null || ! ufw status | grep -q '^Status: active'; then
    echo 'CIDR panel access requires TT_FIREWALL=1 or active UFW.' >&2; exit 1
  fi
fi
apt-get update -qq </dev/null
apt-get install -y -qq --no-install-recommends ca-certificates certbot openssl python3 iproute2 fail2ban curl </dev/null
endpoint_running=0; panel_running=0
systemctl is-active --quiet trusttunnel-managed && endpoint_running=1
systemctl is-active --quiet trusttunnel-admin && panel_running=1
systemctl is-active --quiet trusttunnel-node && panel_running=1
python3 - "$domain" "$role" "$endpoint_running" "$panel_running" <<'PREFLIGHT'
import socket,sys
domain=sys.argv[1]
try:
    socket.getaddrinfo(domain,443,socket.AF_INET)
    held=[]
    ports=[(socket.SOCK_STREAM,80),(socket.SOCK_STREAM,443),(socket.SOCK_DGRAM,443)]
    if sys.argv[2]=='main': ports.append((socket.SOCK_STREAM,8443))
    for kind,port in ports:
        if port==443 and sys.argv[3]=="1": continue
        if port==8443 and sys.argv[4]=="1": continue
        connection=socket.socket(socket.AF_INET,kind)
        connection.bind(('0.0.0.0',port))
        held.append(connection)
except OSError as error:
    raise SystemExit(f'DNS/port preflight failed: {error}. Check the domain and free ports 80/TCP, 443/TCP+UDP, 8443/TCP.')
finally:
    for connection in locals().get('held',[]): connection.close()
PREFLIGHT
getent group trusttunnel >/dev/null || groupadd --system trusttunnel
id trusttunnel >/dev/null 2>&1 || useradd --system --gid trusttunnel --home-dir /var/lib/trusttunnel --no-create-home --shell /usr/sbin/nologin trusttunnel
getent group trusttunnel-admin >/dev/null || groupadd --system trusttunnel-admin
id trusttunnel-admin >/dev/null 2>&1 || useradd --system --gid trusttunnel-admin --home-dir /var/lib/trusttunnel-admin --no-create-home --shell /usr/sbin/nologin trusttunnel-admin
[[ $(id -u trusttunnel) != 0 && $(id -u trusttunnel-admin) != 0 ]] || { echo 'Service account must not map to root.' >&2; exit 1; }
usermod -a -G trusttunnel trusttunnel-admin
# Name every parent explicitly: umask 077 must not leave root:root 0700 ancestors.
install -d -m 0750 -o root -g trusttunnel /opt/trusttunnel /opt/trusttunnel/bin
install -d -m 0750 -o root -g trusttunnel-admin /opt/trusttunnel-admin /opt/trusttunnel-admin/bin
# Never replace a running process's executable, even during partial recovery.
put_binary() {
  local source=$1 target=$2 account=$3 running=$4
  if [[ $running == 1 ]]; then
    cmp -s "$source" "$target" || { echo "Running binary differs: $target; use maintenance update." >&2; exit 1; }
    chown root:"$account" "$target"; chmod 0750 "$target"
  else
    install -m 0750 -o root -g "$account" "$source" "$target"
  fi
}
put_binary "$root/bin/trusttunnel_endpoint" /opt/trusttunnel/bin/trusttunnel_endpoint trusttunnel "$endpoint_running"
put_binary "$root/bin/trusttunnel-admin" /opt/trusttunnel-admin/bin/trusttunnel-admin trusttunnel-admin "$panel_running"
install -m 0700 -o root -g root "$root/backup/trusttunnel-backup" /opt/trusttunnel-admin/bin/trusttunnel-backup
if [[ -f $root/update.sh ]]; then
  install -m 0750 -o root -g root "$root/update.sh" /usr/local/sbin/netlane-update
  # The former command name keeps working for scripts and documentation.
  ln -sf netlane-update /usr/local/sbin/wtt-update
fi
# The console is unprivileged (NoNewPrivileges), so an update it asks for is installed by
# this root timer: the panel writes a request, the timer runs the updater from the channel.
cat >/etc/systemd/system/trusttunnel-update.service <<'UNIT'
[Unit]
Description=Netlane console update request
After=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/netlane-update --requested
UNIT
# The path unit starts the updater as soon as the request appears; the calendar timer is the
# fallback (OnUnitActiveSec could leave the timer without a next run).
cat >/etc/systemd/system/trusttunnel-update.path <<'UNIT'
[Unit]
Description=Start a console update as soon as it is requested

[Path]
PathExists=/var/lib/trusttunnel-admin/update-requested
Unit=trusttunnel-update.service

[Install]
WantedBy=paths.target
UNIT
cat >/etc/systemd/system/trusttunnel-update.timer <<'UNIT'
[Unit]
Description=Read a console update request every two minutes (fallback for the path unit)

[Timer]
OnCalendar=*:0/2
AccuracySec=15s
Unit=trusttunnel-update.service

[Install]
WantedBy=timers.target
UNIT
systemctl daemon-reload
systemctl enable --now trusttunnel-update.path trusttunnel-update.timer >/dev/null 2>&1 || true
if [[ -f $root/geoip-update ]]; then
  install -m 0750 -o root -g root "$root/geoip-update" /usr/local/sbin/geoip-update
fi
[[ -f $root/SOURCE_COMMIT ]] && install -m 0640 -o root -g trusttunnel-admin "$root/SOURCE_COMMIT" /opt/trusttunnel-admin/SOURCE_COMMIT
install -d -m 0750 -o trusttunnel -g trusttunnel /etc/trusttunnel /run/trusttunnel
install -d -m 0700 -o trusttunnel -g trusttunnel /var/lib/trusttunnel /etc/trusttunnel/tls
install -d -m 0700 -o trusttunnel-admin -g trusttunnel-admin /etc/trusttunnel-admin /etc/trusttunnel-admin/tls /var/lib/trusttunnel-admin
# The updater's report: root writes, the console reads (never root writing into the console's directory).
install -d -m 0755 -o root -g root /var/lib/netlane-update /var/lib/netlane-update/report
# Run as the real accounts before certificates, configs or database initialization.
runuser -u trusttunnel -- /opt/trusttunnel/bin/trusttunnel_endpoint --version
runuser -u trusttunnel-admin -- /opt/trusttunnel-admin/bin/trusttunnel-admin version
require_access trusttunnel x /etc/trusttunnel
require_access trusttunnel-admin w /var/lib/trusttunnel-admin


if command -v ufw >/dev/null && ufw status | grep -q '^Status: active'; then
  ufw allow 80/tcp
fi
# An address gets expiry warnings from Let's Encrypt if renewal ever breaks.
if [[ -n ${TT_ACME_EMAIL:-} ]]; then acme_account=(--email "$TT_ACME_EMAIL"); else acme_account=(--register-unsafely-without-email); fi
if ! openssl x509 -checkend 604800 -noout -in "/etc/letsencrypt/live/$domain/fullchain.pem" >/dev/null 2>&1 || ! openssl x509 -checkhost "$domain" -noout -in "/etc/letsencrypt/live/$domain/fullchain.pem" >/dev/null 2>&1; then
certbot certonly --standalone --non-interactive --agree-tos "${acme_account[@]}" --keep-until-expiring -d "$domain" </dev/null
fi
# Check the issued chain and key match before exposing them to either service.
cert_public=$(openssl x509 -in "/etc/letsencrypt/live/$domain/fullchain.pem" -pubkey -noout | openssl pkey -pubin -outform DER | sha256sum)
key_public=$(openssl pkey -in "/etc/letsencrypt/live/$domain/privkey.pem" -pubout -outform DER | sha256sum)
[[ $cert_public == "$key_public" ]] || { echo 'Certificate/private key mismatch.' >&2; exit 1; }
for account in trusttunnel trusttunnel-admin; do
  python3 "$root/install-support.py" file "/etc/$account/tls/fullchain.pem" 0600 "$account" <"/etc/letsencrypt/live/$domain/fullchain.pem"
  python3 "$root/install-support.py" file "/etc/$account/tls/privkey.pem" 0600 "$account" <"/etc/letsencrypt/live/$domain/privkey.pem"
done

python3 "$root/install-support.py" seed
python3 "$root/install-support.py" repair-data
python3 "$root/install-support.py" file /etc/systemd/system/trusttunnel-managed.service 0644 <<'UNIT'
[Unit]
Description=TrustTunnel managed endpoint (dev)
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
User=trusttunnel
Group=trusttunnel
WorkingDirectory=/etc/trusttunnel
ExecStart=/opt/trusttunnel/bin/trusttunnel_endpoint /etc/trusttunnel/vpn.toml /etc/trusttunnel/hosts.toml --control-socket /run/trusttunnel/control.sock --control-state /var/lib/trusttunnel/managed.json
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=3
RuntimeDirectory=trusttunnel
RuntimeDirectoryMode=0750
StateDirectory=trusttunnel
StateDirectoryMode=0700
UMask=0077
LimitNOFILE=65536
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/trusttunnel /run/trusttunnel
[Install]
WantedBy=multi-user.target
UNIT

if [[ $role == node && ! -f /etc/trusttunnel-admin/config.json ]]; then
  runuser -u trusttunnel-admin -- /opt/trusttunnel-admin/bin/trusttunnel-admin join --config /etc/trusttunnel-admin/config.json \
    --url "$TT_JOIN" --domain "$domain" --name "${TT_SERVER_NAME:-}" --country "${TT_SERVER_COUNTRY:-}" \
    --data-dir /var/lib/trusttunnel-admin --endpoint-version "$(/opt/trusttunnel/bin/trusttunnel_endpoint --version 2>/dev/null | head -n1 | awk '{print $NF}')"
fi
if [[ $role == main && ! -f /etc/trusttunnel-admin/config.json ]]; then
  setup_args=(setup --config /etc/trusttunnel-admin/config.json --data-dir /var/lib/trusttunnel-admin --domain "$domain" --control-socket /run/trusttunnel/control.sock)
  if [[ ${TT_PASSWORD_STDIN:-0} == 1 ]]; then setup_args+=(--password-stdin); fi
  runuser -u trusttunnel-admin -- /opt/trusttunnel-admin/bin/trusttunnel-admin "${setup_args[@]}"
fi
python3 "$root/install-support.py" configure
# Validate access to all secrets/data needed by each service without printing secrets.
require_access trusttunnel r /etc/trusttunnel/vpn.toml
require_access trusttunnel r /etc/trusttunnel/hosts.toml
require_access trusttunnel r /etc/trusttunnel/credentials.toml
for account in trusttunnel trusttunnel-admin; do
  require_access "$account" r "/etc/$account/tls/fullchain.pem"
  require_access "$account" r "/etc/$account/tls/privkey.pem"
  require_access "$account" w "/var/lib/$account"
done
require_access trusttunnel-admin r /etc/trusttunnel-admin/config.json
if [[ $role == node ]]; then
python3 "$root/install-support.py" file /etc/systemd/system/trusttunnel-node.service 0644 <<'UNIT'
[Unit]
Description=Netlane node agent and decoy site (dev)
After=network-online.target trusttunnel-managed.service
Wants=network-online.target trusttunnel-managed.service
[Service]
Type=simple
User=trusttunnel-admin
Group=trusttunnel-admin
SupplementaryGroups=trusttunnel
ExecStart=/opt/trusttunnel-admin/bin/trusttunnel-admin node --config /etc/trusttunnel-admin/config.json
Restart=on-failure
RestartSec=3
StateDirectory=trusttunnel-admin
StateDirectoryMode=0700
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/trusttunnel-admin
[Install]
WantedBy=multi-user.target
UNIT
else
python3 "$root/install-support.py" file /etc/systemd/system/trusttunnel-admin.service 0644 <<'UNIT'
[Unit]
Description=Netlane Console (dev)
After=network-online.target trusttunnel-managed.service
Wants=network-online.target trusttunnel-managed.service
[Service]
Type=simple
User=trusttunnel-admin
Group=trusttunnel-admin
SupplementaryGroups=trusttunnel
ExecStart=/opt/trusttunnel-admin/bin/trusttunnel-admin serve --config /etc/trusttunnel-admin/config.json
Restart=on-failure
RestartSec=3
StateDirectory=trusttunnel-admin
StateDirectoryMode=0700
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
# The panel rewrites config.json when the administrator changes the password.
ReadWritePaths=/var/lib/trusttunnel-admin /etc/trusttunnel-admin
[Install]
WantedBy=multi-user.target
UNIT
fi

install -d -m 0755 /etc/letsencrypt/renewal-hooks/deploy
{
# The variable must remain literal in the generated renewal script.
# shellcheck disable=SC2016
printf '#!/bin/sh\nset -eu\n[ "${RENEWED_LINEAGE:-}" = "/etc/letsencrypt/live/%s" ] || exit 0\n' "$domain"
cat <<'HOOK'
python3 - <<'RENEW'
import os, pathlib, pwd, subprocess, tempfile
source=pathlib.Path(os.environ['RENEWED_LINEAGE'])
cert=subprocess.check_output(['openssl','x509','-in',str(source/'fullchain.pem'),'-pubkey','-noout'])
cert=subprocess.run(['openssl','pkey','-pubin','-outform','DER'],input=cert,stdout=subprocess.PIPE,check=True).stdout
key=subprocess.check_output(['openssl','pkey','-in',str(source/'privkey.pem'),'-pubout','-outform','DER'])
if cert!=key: raise SystemExit('Renewed TLS key does not match certificate')
for account in ('trusttunnel','trusttunnel-admin'):
    user=pwd.getpwnam(account)
    for name in ('fullchain.pem','privkey.pem'):
        target=pathlib.Path('/etc')/account/'tls'/name
        if any(p.is_symlink() for p in (target,*target.parents)): raise SystemExit('Refusing TLS destination symlink')
        fd,tmp=tempfile.mkstemp(prefix='.renew-',dir=target.parent)
        try:
            with os.fdopen(fd,'wb') as file:
                os.fchmod(file.fileno(),0o600);os.fchown(file.fileno(),user.pw_uid,user.pw_gid)
                file.write((source/name).read_bytes());file.flush();os.fsync(file.fileno())
            os.replace(tmp,target)
        finally:
            if os.path.exists(tmp): os.unlink(tmp)
RENEW
if systemctl is-active --quiet trusttunnel-managed; then systemctl reload trusttunnel-managed; fi
HOOK
# Only this role's service exists; a missing unit would fail the hook under set -e.
if [[ $role == node ]]; then echo 'systemctl try-restart trusttunnel-node'; else echo 'systemctl try-restart trusttunnel-admin'; fi
} | python3 "$root/install-support.py" file /etc/letsencrypt/renewal-hooks/deploy/trusttunnel-dev 0750
install -m 0644 "$root/backup/trusttunnel-backup.service" /etc/systemd/system/trusttunnel-backup.service
install -m 0644 "$root/backup/trusttunnel-backup.timer" /etc/systemd/system/trusttunnel-backup.timer
# Client flags need the country database; the timer keeps it fresh, and the first
# download happens now so the panel has flags right after installation.
if [[ -f $root/geoip/trusttunnel-geoip.service ]]; then
  install -m 0644 "$root/geoip/trusttunnel-geoip.service" /etc/systemd/system/trusttunnel-geoip.service
  install -m 0644 "$root/geoip/trusttunnel-geoip.timer" /etc/systemd/system/trusttunnel-geoip.timer
fi
if [[ $role == main ]]; then
  install -m 0644 "$root/fail2ban/trusttunnel-admin.filter.conf" /etc/fail2ban/filter.d/trusttunnel-admin.conf
  install -m 0644 "$root/fail2ban/trusttunnel-admin.jail.local" /etc/fail2ban/jail.d/trusttunnel-admin.local
fi
service=trusttunnel-admin
[[ $role == node ]] && service=trusttunnel-node
# BBR with fair queueing usually improves VPN throughput on lossy client networks.
if modprobe tcp_bbr 2>/dev/null && [[ ! -e /etc/sysctl.d/90-trusttunnel.conf ]]; then
  printf '# Netlane: BBR congestion control with fair queueing.\nnet.core.default_qdisc = fq\nnet.ipv4.tcp_congestion_control = bbr\n' >/etc/sysctl.d/90-trusttunnel.conf
  echo tcp_bbr >/etc/modules-load.d/tcp_bbr.conf
  sysctl -q -p /etc/sysctl.d/90-trusttunnel.conf || true
fi
# TT_PANEL_ALLOW (optional): comma-separated IPs or networks allowed to reach the panel on
# 8443; empty keeps the panel on loopback unless TT_PANEL_PUBLIC=1. Scanners then do not see an admin login next to the VPN.
panel_firewall() {
  [[ $role == main ]] || return 0
  if [[ $panel_listen == 127.0.0.1:8443 ]]; then return; fi
  if [[ ${TT_PANEL_PUBLIC:-0} == 1 ]]; then ufw allow 8443/tcp comment 'Netlane Console' >/dev/null; return; fi
  [[ -n ${TT_PANEL_ALLOW:-} ]] || { echo 'Public panel bind without explicit access policy refused.' >&2; exit 1; }
  ufw delete allow 8443/tcp >/dev/null 2>&1 || true
  local source
  for source in ${TT_PANEL_ALLOW//,/ }; do
    ufw allow from "$source" to any port 8443 proto tcp comment 'Netlane Console' >/dev/null
  done
}
if [[ ${TT_FIREWALL:-0} == 1 ]]; then
  # Opt-in: deny incoming except SSH (the port sshd really uses), ACME, VPN and the panel.
  apt-get install -y -qq --no-install-recommends ufw </dev/null
  ssh_port=$(sshd -T 2>/dev/null | awk '$1 == "port" && !found {print $2; found=1}')
  ufw allow "${ssh_port:-22}/tcp" comment 'SSH' >/dev/null
  ufw allow 80/tcp comment 'Lets Encrypt HTTP-01' >/dev/null
  ufw allow 443/tcp comment 'TrustTunnel HTTP/2' >/dev/null
  ufw allow 443/udp comment 'TrustTunnel HTTP/3' >/dev/null
  panel_firewall
  ufw default deny incoming >/dev/null
  ufw default allow outgoing >/dev/null
  ufw --force enable >/dev/null
  if [[ $role == main ]]; then echo "Firewall: UFW active, open ${ssh_port:-22}/tcp, 80/tcp, 443/tcp+udp; panel bind $panel_listen ${TT_PANEL_ALLOW:+only from $TT_PANEL_ALLOW}."; else echo "Firewall: UFW active, open ${ssh_port:-22}/tcp, 80/tcp, 443/tcp+udp."; fi
elif command -v ufw >/dev/null && ufw status | grep -q '^Status: active'; then
  ufw allow 80/tcp
  ufw allow 443/tcp
  ufw allow 443/udp
  panel_firewall
fi
systemctl daemon-reload
systemctl enable --now trusttunnel-managed "$service" certbot.timer trusttunnel-backup.timer
# Flags are a convenience: a failed download must not fail the installation.
if [[ -f /etc/systemd/system/trusttunnel-geoip.timer ]]; then
  systemctl enable --now trusttunnel-geoip.timer || true
  systemctl start trusttunnel-geoip.service || echo 'GeoIP database not installed; the panel will show no flags until geoip-update succeeds.' >&2
fi
systemctl enable fail2ban >/dev/null 2>&1 || true
systemctl restart fail2ban || echo 'fail2ban did not start; check: journalctl -u fail2ban' >&2
# A node is ready after its first sync with main.
for ((attempt=0; attempt<60; attempt++)); do
  if runuser -u trusttunnel-admin -- /opt/trusttunnel-admin/bin/trusttunnel-admin doctor >/dev/null 2>&1; then break; fi
  sleep 1
done
systemctl is-active --quiet trusttunnel-managed
systemctl is-active --quiet "$service"
runuser -u trusttunnel-admin -- /opt/trusttunnel-admin/bin/trusttunnel-admin doctor

# Final diagnostics cover the actual sandboxed units, TLS and socket permissions.
for timer in certbot.timer trusttunnel-backup.timer; do
  systemctl is-enabled --quiet "$timer"
  systemctl is-active --quiet "$timer"
done
# Type=simple may be active before Fail2ban has created its command socket/jails.
for ((attempt=0; attempt<30; attempt++)); do
  if fail2ban-client ping >/dev/null 2>&1 && { [[ $role == node ]] || fail2ban-client status trusttunnel-admin >/dev/null 2>&1; }; then break; fi
  sleep 1
done
fail2ban-client ping
if [[ $role == main ]]; then fail2ban-client status trusttunnel-admin >/dev/null; fi
require_access trusttunnel-admin srw /run/trusttunnel/control.sock
curl --fail --silent --show-error --retry 10 --retry-connrefused --retry-delay 1 --max-time 10 --resolve "$domain:443:127.0.0.1" "https://$domain/" >/dev/null
if [[ $role == main ]]; then
  curl --fail --silent --show-error --retry 10 --retry-connrefused --retry-delay 1 --max-time 10 --resolve "$domain:8443:127.0.0.1" "https://$domain:8443/healthz" | python3 -c 'import json,sys; assert json.load(sys.stdin).get("status")=="ok", "HTTPS health check failed"'
fi
python3 "$root/install-support.py" complete
trap - ERR
printf 'Daily backups: /var/backups/trusttunnel (04:00 MSK, 14 kept). Copy them off the VPS.\n'
if [[ $role == node ]]; then
  main_url=$(python3 -c 'import json;print(json.load(open("/etc/trusttunnel-admin/config.json"))["main_url"])')
  printf 'Node: %s:443 TCP/UDP, users come from %s\n' "$domain" "$main_url"
  echo 'Installation complete. Users are managed in the main server panel.'
  exit 0
fi
[[ $panel_listen != 127.0.0.1:8443 ]] || echo "Panel is loopback-only. Use SSH forwarding: ssh -L 8443:127.0.0.1:8443 root@$domain"
printf 'Panel: https://%s:8443/\nAPI: https://%s:8443/api/v1/\nVPN: %s:443 TCP/UDP\n' "$domain" "$domain" "$domain"
echo 'Installation complete. Sign in to the panel to create your first VPN user. Demo screens are separate at /preview/.'
