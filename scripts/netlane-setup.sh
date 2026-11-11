#!/usr/bin/env bash
# Netlane: interactive setup of a fresh VPS (Ubuntu 24.04/26.04, Debian 13, x86_64).
#
# Run as root on the new server. It asks every question first, then prepares the whole
# server without stopping: updates, time sync, swap, network tuning, the VPN endpoint,
# the panel with its TLS certificate, UFW, fail2ban, SSH hardening and daily backups.
#
#   mkdir netlane && tar -xzf trusttunnel-dev.tar.gz -C netlane && sudo bash netlane/setup.sh   (inside the bundle)
#   sudo bash netlane-setup.sh            (alone: uses trusttunnel-dev.tar.gz next to it, or
#                                      downloads the latest release of divezh/netlane-server)
#
# Unattended: NETLANE_YES=1 NETLANE_DOMAIN=vpn.example.com [NETLANE_EMAIL=] [NETLANE_SSH_KEY='ssh-ed25519 …']
#   [NETLANE_SERVER_NAME=Амстердам] [NETLANE_SERVER_COUNTRY=NL|''] (country defaults to the one of the server IP)
#   [NETLANE_PANEL_ALLOW=203.0.113.7,198.51.100.0/24] [NETLANE_PANEL_PUBLIC=1] (default: loopback only)
# Recovery: bash setup.sh --resume. Existing configs and passwords are retained.
#   [NETLANE_JOIN='https://<main>/<prefix>/node/join#<code>'] or --join '…' (this server becomes a node)
#   [NETLANE_SSH_PASSWORDS=off|keep] [NETLANE_UPGRADE=1|0] [NETLANE_AUTO_UPDATES=1|0] [NETLANE_SWAP=1|0]
#   The panel password is then generated and printed once at the end.
set -Eeuo pipefail
umask 077
export DEBIAN_FRONTEND=noninteractive
REPO=${NETLANE_REPO:-divezh/netlane-server}

bold=$'\e[1m' dim=$'\e[2m' red=$'\e[31m' green=$'\e[32m' yellow=$'\e[33m' reset=$'\e[0m'
[[ -t 1 ]] || { bold='' dim='' red='' green='' yellow='' reset=''; }
say() { printf '%s\n' "$*"; }
step() { printf '\n%s==> %s%s\n' "$bold" "$*" "$reset"; }
warn() { printf '%s! %s%s\n' "$yellow" "$*" "$reset" >&2; }
die() { printf '%sОшибка: %s%s\n' "$red" "$*" "$reset" >&2; exit 1; }
# Never log BASH_COMMAND or variables here: they may contain passwords or join tokens.
setup_error() { printf 'Ошибка установщика: строка %s, код %s.\n' "$2" "$1" >&2; }
trap 'setup_error "$?" "$LINENO"' ERR

ask() { # ask VAR "question" default
  local answer
  read -r -p "$2${3:+ [$3]}: " answer </dev/tty
  printf -v "$1" '%s' "${answer:-$3}"
}
ask_yes() { # ask_yes "question" y|n -> status 0 for yes
  local answer hint=y/N
  [[ $2 == y ]] && hint=Y/n
  while true; do
    read -r -p "$1 [$hint]: " answer </dev/tty
    answer=${answer:-$2}
    case ${answer,,} in y|yes|д|да) return 0 ;; n|no|н|нет) return 1 ;; esac
  done
}
ask_secret() { # ask_secret VAR "question"
  local answer
  read -r -s -p "$2: " answer </dev/tty
  printf '\n' >/dev/tty
  printf -v "$1" '%s' "$answer"
}
yes_env() { [[ ${1:-} == 1 || ${1,,} == yes || ${1,,} == y ]]; }

# Variables of the former product name keep working: WTT_DOMAIN is treated as NETLANE_DOMAIN.
for name in YES DOMAIN EMAIL SSH_KEY SERVER_NAME SERVER_COUNTRY PANEL_ALLOW PANEL_PUBLIC JOIN SSH_PASSWORDS UPGRADE AUTO_UPDATES SWAP IGNORE_DNS RESUME GITHUB_TOKEN; do
  current=NETLANE_$name legacy=WTT_$name
  [[ -n ${!current:-} || -z ${!legacy:-} ]] || printf -v "$current" '%s' "${!legacy}"
done

# --join '<link from the panel>' makes this server a node of a main server.
resume=${NETLANE_RESUME:-0}
join_url=${NETLANE_JOIN:-}
while (($#)); do
  case $1 in
    --resume) resume=1; shift ;;
    --join) join_url=${2:-}; shift $(( $# > 1 ? 2 : 1 )) ;;
    --join=*) join_url=${1#--join=}; shift ;;
    *) die "неизвестный параметр: $1 (есть --join и --resume)" ;;
  esac
done

# ---- 1. Is this server suitable? Checked before any question. ----------------------------
((EUID == 0)) || die 'нужен root: запустите через sudo.'
[[ $(uname -m) == x86_64 ]] || die 'нужен сервер x86_64.'
[[ -d /run/systemd/system ]] || die 'нужен сервер с systemd (контейнеры без systemd не подходят).'
os_id=$(sed -n 's/^ID=//p' /etc/os-release | tr -d '"')
os_version=$(sed -n 's/^VERSION_ID=//p' /etc/os-release | tr -d '"')
os_name=$(sed -n 's/^PRETTY_NAME=//p' /etc/os-release | tr -d '"')
case "$os_id:$os_version" in ubuntu:24.04|ubuntu:26.04|debian:13) ;; *) die "система $os_name не поддерживается: нужна Ubuntu 24.04/26.04 или Debian 13." ;; esac
[[ $resume == 0 || $resume == 1 ]] || die 'NETLANE_RESUME должен быть 0 или 1.'
resume_role=''
for existing in /etc/trusttunnel/vpn.toml /etc/trusttunnel-admin/config.json /var/lib/trusttunnel/managed.json /var/lib/trusttunnel-admin/panel.sqlite; do
  [[ ! -e $existing || $resume == 1 ]] || die "уже есть $existing; для незавершённой установки используйте --resume, для рабочей — netlane-update."
done
state_file=/var/lib/netlane-install/state.json
[[ -f $state_file ]] || state_file=/var/lib/wtt-install/state.json
if [[ -f $state_file ]]; then
  command -v python3 >/dev/null || die 'для проверки состояния нужен python3; сервер не изменён.'
  python3 -c 'import json,sys; s=json.load(open(sys.argv[1])); assert not s.get("complete"), "Установка уже завершена: используйте netlane-update"' "$state_file"
  [[ $resume == 1 ]] || die 'предыдущая установка прервана: запустите с --resume.'
  field() { python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get(sys.argv[2],""))' "$state_file" "$1"; }
  NETLANE_DOMAIN=${NETLANE_DOMAIN:-$(field domain)}
  resume_role=$(field role)
  if [[ $resume_role == node && -z $join_url && -f /var/lib/trusttunnel-admin/node.token && ! -f /etc/trusttunnel-admin/config.json ]]; then
    saved_main=$(field main_url)
    [[ -z $saved_main ]] || join_url="$saved_main/node/join#resume"
  fi
  NETLANE_PANEL_ALLOW=${NETLANE_PANEL_ALLOW-$(field panel_allow)}
  NETLANE_PANEL_PUBLIC=${NETLANE_PANEL_PUBLIC-$(field panel_public)}
elif [[ $resume == 1 ]]; then
  for unit in trusttunnel-managed trusttunnel-admin trusttunnel-node; do
    systemctl is-active --quiet "$unit" && die 'рабочая установка без маркера: автоматическое восстановление запрещено.'
  done
  if [[ -f /etc/trusttunnel-admin/config.json ]]; then
    NETLANE_DOMAIN=${NETLANE_DOMAIN:-$(python3 -c 'import json;print(json.load(open("/etc/trusttunnel-admin/config.json"))["domain"])')}
    resume_role=$(python3 -c 'import json;print(json.load(open("/etc/trusttunnel-admin/config.json")).get("role") or "main")')
  fi
fi
# Package output goes to a log; on failure its end is shown.
apt_log=/var/log/netlane-setup-apt.log
apt_get() {
  if ! apt-get -y -q -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold "$@" </dev/null >>"$apt_log" 2>&1; then
    tail -n 25 "$apt_log" >&2
    die "apt-get $1 не удался, полный журнал: $apt_log"
  fi
}
# Questions go to the terminal even when the script itself comes from a pipe.
interactive=0
if [[ ${NETLANE_YES:-0} != 1 ]]; then
  if [[ -r /dev/tty ]] && { : </dev/tty; } 2>/dev/null; then interactive=1; else die 'нет терминала для вопросов. Запустите в SSH-сессии или задайте NETLANE_YES=1 и NETLANE_DOMAIN.'; fi
fi
# curl finds the public address, python3 reads the GitHub release; minimal images may lack them.
step 'Проверка сервера и необходимых программ'
if ! command -v curl >/dev/null || ! command -v python3 >/dev/null; then
  apt_get update && apt_get install --no-install-recommends curl ca-certificates python3
fi

# Public IPv4: ask outside services, fall back to the address of the default route.
public_ip=''
for service in https://api.ipify.org https://ifconfig.me/ip https://icanhazip.com; do
  candidate=$(curl -fsS4 --max-time 5 "$service" 2>/dev/null | tr -d '[:space:]') || true
  if [[ $candidate =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]; then public_ip=$candidate; break; fi
done
[[ -n $public_ip ]] || public_ip=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{for (i = 1; i < NF; i++) if ($i == "src" && !found) {print $(i + 1); found=1}}')
mem_mb=$(awk '/^MemTotal:/ {print int($2 / 1024)}' /proc/meminfo)
swap_mb=$(awk '/^SwapTotal:/ {print int($2 / 1024)}' /proc/meminfo)
disk_free_mb=$(df -Pm / | awk 'NR == 2 {print $4}')
ssh_port=$(sshd -T 2>/dev/null | awk '$1 == "port" && !found {print $2; found=1}')
ssh_port=${ssh_port:-22}
owner=${SUDO_USER:-root}

cat <<EOF

${bold}Netlane — установка на новый сервер${reset}
Система: $os_name, память ${mem_mb} МБ, свободно на диске ${disk_free_mb} МБ.
Внешний IPv4: ${public_ip:-не определён}. SSH: порт $ssh_port, пользователь $owner.

Скрипт поставит VPN-сервер TrustTunnel и панель Netlane Console, получит
сертификат Let's Encrypt, включит UFW и fail2ban, усилит SSH и настроит
ежедневные резервные копии. Сначала несколько вопросов, затем всё идёт само.
EOF

# ---- 2. Questions ---------------------------------------------------------------------------
# Main server with the panel, or a node that takes its users from a main server.
valid_join() { [[ $1 =~ ^https://[^/]+/[a-z2-7]{26}/node/join#[A-Za-z0-9_-]+$ ]]; }
role=${resume_role:-main}
if [[ -n $join_url ]]; then
  role=node
elif ((interactive)) && [[ -z $resume_role ]]; then
  say ''
  say 'Что ставим на этот сервер?'
  say '  1 — главный сервер с панелью'
  say '  2 — ноду: ещё одну локацию для уже установленного главного сервера'
  role_choice=''
  while true; do
    ask role_choice 'Выбор' 1
    case $role_choice in 1) break ;; 2) role=node; break ;; esac
  done
fi
main_host=''
if [[ $role == node ]]; then
  while [[ ! ( $resume == 1 && -f /etc/trusttunnel-admin/config.json ) ]] && ! valid_join "$join_url"; do
    ((interactive)) || die 'NETLANE_JOIN: нужна ссылка из панели главного сервера («Ноды → Добавить ноду»).'
    [[ -n $join_url ]] && warn 'Это не ссылка подключения ноды. Скопируйте её из команды в панели целиком, вместе с частью после #.'
    say "${dim}В панели главного сервера: «Ноды → Добавить ноду». Ссылка — в кавычках после --join.${reset}"
    ask join_url 'Ссылка подключения (https://…/node/join#…)' ''
  done
  if [[ -n $join_url ]]; then main_host=${join_url#https://}; main_host=${main_host%%/*}; else
    main_host=$(python3 -c 'import json,urllib.parse;print(urllib.parse.urlparse(json.load(open("/etc/trusttunnel-admin/config.json"))["main_url"]).hostname)')
  fi
fi
valid_domain() { [[ $1 =~ ^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$ ]] && ((${#1} <= 253)); }
domain_ips() { getent ahostsv4 "$1" 2>/dev/null | awk '{print $1}' | sort -u | tr '\n' ' '; }

domain=${NETLANE_DOMAIN:-}
while true; do
  if ((interactive)); then
    say ''
    if [[ $role == node ]]; then
      say "Домен этой ноды, например de.example.com (не домен главного сервера). A-запись должна указывать на ${public_ip:-этот сервер}."
    else
      say "Домен для VPN и панели, например vpn.example.com. A-запись должна указывать на ${public_ip:-этот сервер}."
    fi
    say "${dim}В Cloudflare — только «DNS only» (серое облако): VPN не работает через прокси.${reset}"
    ask domain 'Домен' "$domain"
  fi
  domain=${domain,,}
  if ! valid_domain "$domain"; then
    ((interactive)) || die "неверный домен: '$domain'."
    warn 'Нужен домен латиницей, например vpn.example.com.'; domain=''; continue
  fi
  # Words that block lists and searches look for give the server away.
  if [[ $domain =~ (vpn|tunnel|proxy|xray|vless|wireguard|socks|node) ]]; then
    warn "В домене есть слово «${BASH_REMATCH[1]}»: по таким словам VPN-серверы находят и блокируют."
    warn 'Лучше нейтральное имя, например static., app. или cdn. на домене без таких слов.'
    if ((interactive)) && ! ask_yes 'Всё равно использовать этот домен?' n; then domain=''; continue; fi
  fi
  resolved=$(domain_ips "$domain"); resolved=${resolved% }
  if [[ -z $resolved ]]; then
    ((interactive)) || die "домен $domain не разрешается в IPv4. Создайте A-запись и повторите."
    warn "Домен $domain пока не разрешается в IPv4. Создайте A-запись на ${public_ip:-IP сервера}, подождите пару минут."
    if ask_yes 'Проверить ещё раз?' y; then continue; fi
    die 'без A-записи сертификат не выдать.'
  fi
  if [[ -n $public_ip && " $resolved " != *" $public_ip "* ]]; then
    warn "Домен $domain указывает на $resolved, а у сервера $public_ip."
    warn 'Let'"'"'s Encrypt не выдаст сертификат, если домен ведёт не сюда (или включён прокси Cloudflare).'
    if ((interactive)); then
      ask_yes 'Проверить DNS ещё раз?' y && continue
      ask_yes 'Всё равно продолжить (например, сервер за NAT)?' n || die 'исправьте A-запись и запустите скрипт снова.'
    else
      yes_env "${NETLANE_IGNORE_DNS:-0}" || die 'A-запись ведёт не на этот сервер (NETLANE_IGNORE_DNS=1, чтобы продолжить).'
    fi
  fi
  break
done

# Name and country shown in client profiles (flag + name); the country is suggested from the IP.
valid_server_name() { [[ -n $1 && ${#1} -le 64 && $1 != *[[:cntrl:]]* ]]; }
suggested_country=''
if [[ -n $public_ip ]]; then
  suggested_country=$(curl -fsS --max-time 5 "https://ipinfo.io/$public_ip/country" 2>/dev/null | tr -d '[:space:]') || true
  [[ $suggested_country =~ ^[A-Z]{2}$ ]] || suggested_country=''
fi
server_name=${NETLANE_SERVER_NAME:-$domain}
server_country=${NETLANE_SERVER_COUNTRY-$suggested_country}
if ((interactive)); then
  say ''
  say 'Название и страна сервера: клиенты показывают их в списке серверов с флагом страны.'
  while true; do
    ask server_name 'Название сервера' "$server_name"
    server_name=$(printf '%s' "$server_name" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
    valid_server_name "$server_name" && break
    warn 'Название — от 1 до 64 символов.'; server_name=$domain
  done
  while true; do
    ask server_country "Страна (две буквы, Enter — ${suggested_country:-без флага})" "$suggested_country"
    server_country=${server_country^^}
    [[ -z $server_country || $server_country =~ ^[A-Z]{2}$ ]] && break
    warn 'Код страны — две латинские буквы, например NL или DE.'
  done
else
  server_country=${server_country^^}
  valid_server_name "$server_name" || die "неверное название сервера: '$server_name'."
  [[ -z $server_country || $server_country =~ ^[A-Z]{2}$ ]] || die "неверный код страны: '$server_country'."
fi

# Panel access is private by default; external access requires an explicit choice.
panel_public=${NETLANE_PANEL_PUBLIC:-0}
[[ $panel_public == 0 || $panel_public == 1 ]] || die 'NETLANE_PANEL_PUBLIC должен быть 0 или 1.'
panel_allow=${NETLANE_PANEL_ALLOW:-}
if [[ $role == main ]]; then
admin_ip=${SSH_CLIENT:-}; admin_ip=${admin_ip%% *}
[[ $admin_ip =~ ^[0-9.]+$|^[0-9a-fA-F:]+$ ]] || admin_ip=''
valid_sources() { python3 -c 'import ipaddress,sys
for item in sys.argv[1].split(","):
    ipaddress.ip_network(item.strip(), strict=False)' "$1" 2>/dev/null; }
if ((interactive)); then
  say ''
  say 'Кто может открывать панель (порт 8443)? Если всем — сканеры увидят страницу входа рядом с VPN.'
  if [[ -n $admin_ip ]]; then say "  1 — только с вашего текущего IP ($admin_ip)"; fi
  say '  2 — с указанных адресов'
  say '  3 — всем (публичный порт)'
  say '  4 — только локально, через SSH-туннель'
  panel_choice=''
  while true; do
    ask panel_choice 'Выбор' "$( [[ -n $admin_ip ]] && echo 1 || echo 4)"
    case $panel_choice in
      1) [[ -n $admin_ip ]] && { panel_allow=$admin_ip; panel_public=0; break; } ;;
      2) ask panel_allow 'IP или сети через запятую, например 203.0.113.7,198.51.100.0/24' ''
         panel_allow=${panel_allow// /}
         valid_sources "$panel_allow" && { panel_public=0; break; }
         warn 'Нужны IP-адреса или сети через запятую.' ;;
      3) panel_allow=''; panel_public=1; break ;;
      4) panel_allow=''; panel_public=0; break ;;
    esac
  done
  if [[ -n $panel_allow ]]; then say "${dim}Если ваш IP сменится, откройте панель по SSH: ufw allow from НОВЫЙ_IP to any port 8443 proto tcp${reset}"; fi
elif [[ -n $panel_allow ]]; then
  panel_allow=${panel_allow// /}
  valid_sources "$panel_allow" || die "NETLANE_PANEL_ALLOW: нужны IP или сети через запятую, а не '$panel_allow'."
fi
else
  panel_allow=''
fi

email=${NETLANE_EMAIL:-}
if ((interactive)); then
  say ''
  say 'Почта для Let'"'"'s Encrypt: туда придёт письмо, если автопродление сертификата сломается.'
  ask email 'Почта (Enter — без почты)' "$email"
fi
if [[ -n $email && ! $email =~ ^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$ ]]; then
  ((interactive)) && warn 'Адрес не похож на почту, продолжаю без неё.'
  email=''
fi

panel_password='' confirm=''
generated_password=1
password_reused=0
[[ ! -f /etc/trusttunnel-admin/config.json ]] || password_reused=1
if ((interactive)) && [[ $role == main && $password_reused == 0 ]]; then
  say ''
  say 'Пароль администратора панели (логин admin). От 12 символов.'
  while true; do
    ask_secret panel_password 'Пароль (Enter — сгенерировать надёжный)'
    [[ -z $panel_password ]] && break
    if ((${#panel_password} < 12 || ${#panel_password} > 1024)); then warn 'Нужно от 12 до 1024 символов.'; continue; fi
    ask_secret confirm 'Повторите пароль'
    [[ $confirm == "$panel_password" ]] && { generated_password=0; break; }
    warn 'Пароли не совпадают.'
  done
  unset confirm
fi
# 144 random bits as hex when no password was given.
[[ -n $panel_password || $password_reused == 1 || $role == node ]] || panel_password=$(od -An -N18 -tx1 /dev/urandom | tr -d ' \n')

valid_key() { [[ $1 =~ ^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com)\ [A-Za-z0-9+/=]+(\ .*)?$ ]]; }
owner_home=$(getent passwd "$owner" | cut -d: -f6)
has_keys=0
[[ -s $owner_home/.ssh/authorized_keys ]] && grep -qE '^(ssh-|ecdsa-|sk-)' "$owner_home/.ssh/authorized_keys" && has_keys=1
ssh_key=${NETLANE_SSH_KEY:-}
if ((interactive)); then
  say ''
  say "Публичный SSH-ключ для входа $owner без пароля (строка из ~/.ssh/id_ed25519.pub)."
  ((has_keys)) && say "${dim}У $owner уже есть ключи; можно добавить ещё один или нажать Enter.${reset}"
  while true; do
    ask ssh_key 'Ключ (Enter — пропустить)' ''
    [[ -z $ssh_key ]] || valid_key "$ssh_key" && break
    warn 'Это не публичный ключ OpenSSH. Вставьте одну строку, начинающуюся с ssh-ed25519 или ssh-rsa.'
  done
elif [[ -n $ssh_key ]] && ! valid_key "$ssh_key"; then
  die 'NETLANE_SSH_KEY не похож на публичный ключ OpenSSH.'
fi

disable_passwords=0
if [[ -n $ssh_key ]] || ((has_keys)); then
  if ((interactive)); then
    say ''
    say 'Вход по SSH-паролю — главная цель перебора. С ключом пароль можно отключить.'
    say "${dim}Перед этим убедитесь, что входите по ключу: иначе потеряете доступ к серверу (останется только консоль хостера).${reset}"
    ask_yes 'Отключить вход по паролю в SSH?' n && disable_passwords=1
  else
    [[ ${NETLANE_SSH_PASSWORDS:-keep} == off ]] && disable_passwords=1
  fi
fi

upgrade=1 auto_updates=1 swap=0
if ((interactive)); then
  say ''
  ask_yes 'Обновить все пакеты системы сейчас (рекомендуется на новом сервере)?' y || upgrade=0
  ask_yes 'Ставить обновления безопасности автоматически (без автоперезагрузки)?' y || auto_updates=0
else
  yes_env "${NETLANE_UPGRADE:-1}" || upgrade=0
  yes_env "${NETLANE_AUTO_UPDATES:-1}" || auto_updates=0
fi
if ((swap_mb == 0 && mem_mb < 2048 && disk_free_mb > 4096)); then
  if ((interactive)); then
    ask_yes "Памяти ${mem_mb} МБ и нет подкачки. Создать файл подкачки 1 ГБ?" y && swap=1
  else
    yes_env "${NETLANE_SWAP:-1}" && swap=1
  fi
fi

# ---- 3. Install bundle: next to the script, as a tarball, or the latest GitHub release --------
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
work=$(mktemp -d /root/netlane-setup.XXXXXX)
cleanup() { rm -rf -- "$work"; }
trap cleanup EXIT
bundle=''
if [[ -x $here/install.sh && -x $here/bin/trusttunnel-admin ]]; then
  bundle=$here
elif [[ -f $here/trusttunnel-dev.tar.gz ]]; then
  [[ -f $here/trusttunnel-dev.tar.gz.sha256 ]] || die "рядом с trusttunnel-dev.tar.gz нет trusttunnel-dev.tar.gz.sha256."
  (cd "$here" && sha256sum -c --quiet trusttunnel-dev.tar.gz.sha256) || die 'контрольная сумма комплекта не совпала.'
  tar -xzf "$here/trusttunnel-dev.tar.gz" -C "$work"
  bundle=$work
else
  ((interactive)) && say '' "Комплекта рядом со скриптом нет: скачаю последний релиз $REPO."
fi

if ((interactive)); then
  cat <<EOF

${bold}Проверьте перед установкой${reset}
  Домен:              $domain${resolved:+ ($resolved)}
  Название:            $server_name
  Страна:              ${server_country:-без флага}
  Роль:                $( [[ $role == node ]] && echo "нода главного сервера $main_host" || echo 'главный сервер с панелью')
  Панель доступна:     $( [[ $role == node ]] && echo 'нет, пользователи — в панели главного сервера' || echo "${panel_allow:-$( [[ $panel_public == 1 ]] && echo всем || echo 'локально через SSH-туннель' )}")
  Почта Let's Encrypt: ${email:-без почты}
  Пароль панели:       $( if [[ $role == node ]]; then echo 'не нужен'; elif ((password_reused)); then echo 'существующий, не изменится'; elif ((generated_password)); then echo 'будет сгенерирован и показан в конце'; else echo 'задан вами'; fi )
  SSH-ключ:            $( [[ -n $ssh_key ]] && echo "добавить для $owner" || echo 'не добавлять')
  Вход по паролю SSH:  $( ((disable_passwords)) && echo 'отключить' || echo 'оставить')
  Обновить систему:    $( ((upgrade)) && echo да || echo нет); автообновления безопасности: $( ((auto_updates)) && echo да || echo нет)
  Файл подкачки 1 ГБ:  $( ((swap)) && echo создать || echo нет)
  Откроются порты:     $ssh_port/tcp (SSH), 80/tcp (Let's Encrypt), 443/tcp+udp (VPN)$( [[ $role == main ]] && echo ", панель $( [[ -n $panel_allow || $panel_public == 1 ]] && echo "8443/tcp${panel_allow:+, только с $panel_allow}" || echo 'только локально' )")
EOF
  ask_yes 'Начать установку?' y || die 'установка отменена, сервер не изменён.'
fi
started=$(date +%s)

if [[ -z $bundle ]]; then
  step 'Скачиваю последний релиз'
  tag=$(curl -fsS --proto '=https' -H 'Accept: application/vnd.github+json' "https://api.github.com/repos/$REPO/releases/latest" |
    python3 -c 'import json,sys; print(json.load(sys.stdin)["tag_name"])') || die "GitHub не отдал последний релиз $REPO."
  [[ $tag =~ ^[A-Za-z0-9._-]+$ ]] || die "неожиданный тег релиза: $tag"
  for name in trusttunnel-dev.tar.gz trusttunnel-dev.tar.gz.sha256; do
    curl -fsSL --proto '=https' --max-time 600 -o "$work/$name" "https://github.com/$REPO/releases/download/$tag/$name" ||
      die "в релизе $tag нет файла $name."
  done
  (cd "$work" && sha256sum -c --quiet trusttunnel-dev.tar.gz.sha256) || die 'контрольная сумма скачанного комплекта не совпала.'
  mkdir "$work/bundle" && tar -xzf "$work/trusttunnel-dev.tar.gz" -C "$work/bundle"
  bundle=$work/bundle
  say "Релиз $tag, SHA-256 совпала."
fi
[[ -x $bundle/install.sh && -x $bundle/bin/trusttunnel-admin && -x $bundle/bin/trusttunnel_endpoint ]] || die 'комплект неполный.'

# Validate recovery identity and panel policy before package/network/SSH changes.
[[ -f $bundle/install-support.py ]] || die 'комплект не содержит install-support.py.'
requested_listen=127.0.0.1:8443
[[ $panel_public == 0 && -z $panel_allow ]] || requested_listen=0.0.0.0:8443
TT_RESUME="$resume" TT_PANEL_ALLOW="$panel_allow" TT_PANEL_PUBLIC="$panel_public" python3 "$bundle/install-support.py" preflight "$domain" "$role" "$requested_listen"

# ---- 4. Base system -------------------------------------------------------------------------
step 'Пакеты'
apt_get update
((upgrade)) && { say 'Обновляю систему (может занять несколько минут)…'; apt_get full-upgrade; }
apt_get install --no-install-recommends ca-certificates curl ufw fail2ban python3 iproute2 openssl
if ((auto_updates)); then
  apt_get install --no-install-recommends unattended-upgrades
  cat >/etc/apt/apt.conf.d/20auto-upgrades <<'EOF'
// Netlane: daily package lists and security updates, no automatic reboot.
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::AutocleanInterval "7";
EOF
  printf '// Netlane\nUnattended-Upgrade::Automatic-Reboot "false";\n' >/etc/apt/apt.conf.d/52netlane-unattended
  say 'Автообновления безопасности включены.'
fi

step 'Время и журнал'
# TLS and certificate renewal need a correct clock.
timedatectl set-ntp true 2>/dev/null || warn 'не удалось включить синхронизацию времени (timedatectl).'
install -d -m 0755 /etc/systemd/journald.conf.d
printf '# Netlane: keep the system journal small on a VPS disk.\n[Journal]\nSystemMaxUse=200M\n' >/etc/systemd/journald.conf.d/90-netlane.conf
systemctl restart systemd-journald
say "Часы: $(timedatectl show -p NTPSynchronized --value 2>/dev/null | sed 's/yes/синхронизированы/;s/no/синхронизируются/'); журнал не больше 200 МБ."

if ((swap)); then
  step 'Файл подкачки'
  if [[ ! -e /swapfile ]]; then
    fallocate -l 1G /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count=1024 status=none
    chmod 0600 /swapfile
    mkswap -q /swapfile
    swapon /swapfile
    grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' >>/etc/fstab
    say 'Создан /swapfile 1 ГБ.'
  else
    warn '/swapfile уже есть, не трогаю.'
  fi
fi

step 'Сеть'
# BBR and fair queueing are set by install.sh. These help QUIC (large UDP buffers), many
# clients (backlog) and paths that drop ICMP (MTU probing). Values are conservative.
cat >/etc/sysctl.d/91-netlane-net.conf <<'EOF'
# Netlane network tuning.
net.core.rmem_max = 7500000
net.core.wmem_max = 7500000
net.core.netdev_max_backlog = 16384
net.core.somaxconn = 4096
net.ipv4.tcp_max_syn_backlog = 8192
net.ipv4.tcp_mtu_probing = 1
net.ipv4.tcp_fastopen = 3
net.ipv4.tcp_slow_start_after_idle = 0
net.ipv4.ip_local_port_range = 10240 65535
EOF
((swap)) && printf 'vm.swappiness = 10\n' >>/etc/sysctl.d/91-netlane-net.conf
if sysctl -q -p /etc/sysctl.d/91-netlane-net.conf 2>/dev/null; then say 'Параметры сети применены.'; else warn 'часть сетевых параметров хостинг не разрешает менять; остальные применены.'; fi

# ---- 5. VPN, panel, certificate, UFW, fail2ban and backups (install.sh) ----------------------
if [[ $role == node ]]; then
  step 'VPN-нода'
  say "Ставлю VPN-сервер, получаю сертификат Let's Encrypt и подключаю ноду к $main_host…"
else
  step 'VPN-сервер и панель'
  say 'Ставлю VPN-сервер, панель и получаю сертификат Let'"'"'s Encrypt…'
fi
install_log=/var/log/netlane-setup-install.log
if ! printf '%s\n' "$panel_password" | TT_RESUME="$resume" TT_PANEL_PUBLIC="$panel_public" TT_ROLE="$role" TT_JOIN="$join_url" TT_PASSWORD_STDIN=1 TT_FIREWALL=1 TT_ACME_EMAIL="$email" TT_SERVER_NAME="$server_name" TT_SERVER_COUNTRY="$server_country" TT_PANEL_ALLOW="$panel_allow" bash "$bundle/install.sh" "$domain" >"$install_log" 2>&1; then
  tail -n 40 "$install_log" >&2
  die "установка VPN и панели не завершилась, полный журнал: $install_log"
fi
grep -E '^(Конфигурация корректна|Управляемый endpoint|Firewall:|Нода подключена|Последняя синхронизация)' "$install_log" || true

# ---- 6. SSH ---------------------------------------------------------------------------------
step 'SSH'
if [[ -n $ssh_key ]]; then
  group=$(id -gn "$owner")
  install -d -m 0700 -o "$owner" -g "$group" "$owner_home/.ssh"
  touch "$owner_home/.ssh/authorized_keys"
  chown "$owner:$group" "$owner_home/.ssh/authorized_keys"
  chmod 0600 "$owner_home/.ssh/authorized_keys"
  if grep -qxF -- "$ssh_key" "$owner_home/.ssh/authorized_keys"; then say "Ключ уже был у $owner."; else printf '%s\n' "$ssh_key" >>"$owner_home/.ssh/authorized_keys"; say "Ключ добавлен для $owner."; fi
fi
# sshd keeps the first value it reads and includes sshd_config.d/*.conf in name order, so
# 01- wins over the cloud images' 50-cloud-init.conf (which often enables passwords).
hardening=/etc/ssh/sshd_config.d/01-netlane.conf
had_hardening=0
if [[ -e $hardening ]]; then
  [[ ! -L $hardening ]] || die 'SSH hardening path is a symlink; review manually.'
  cp -p "$hardening" "$work/sshd-previous.conf"
  hardening_mode=$(stat -c %a "$hardening")
  had_hardening=1
fi
{
  echo '# Netlane SSH hardening.'
  echo 'MaxAuthTries 3'
  echo 'LoginGraceTime 30'
  echo 'X11Forwarding no'
  if ((disable_passwords)); then
    echo 'PasswordAuthentication no'
    echo 'KbdInteractiveAuthentication no'
    echo 'PermitRootLogin prohibit-password'
  fi
} >"$work/sshd-new.conf"
python3 "$bundle/install-support.py" file "$hardening" 0644 <"$work/sshd-new.conf"
if sshd -t 2>/dev/null; then
  systemctl reload ssh 2>/dev/null || systemctl reload sshd
  if ((disable_passwords)) && [[ $(sshd -T 2>/dev/null | awk '$1 == "passwordauthentication" {print $2}') != no ]]; then
    warn 'Вход по паролю остался включён: его включает другой файл настроек SSH. Проверьте: sshd -T | grep passwordauth'
  fi
  say "Вход: не больше 3 попыток за подключение$( ((disable_passwords)) && echo ', только по ключу')."
else
  if ((had_hardening)); then
    python3 "$bundle/install-support.py" file "$hardening" "$hardening_mode" <"$work/sshd-previous.conf"
  else
    rm -f -- "$hardening"
  fi
  warn 'sshd отклонил настройки, SSH оставлен как был.'
fi

# ---- 7. Check and summary -------------------------------------------------------------------
step 'Проверка'
problems=0
service=trusttunnel-admin
[[ $role == node ]] && service=trusttunnel-node
for unit in trusttunnel-managed "$service" fail2ban; do
  if systemctl is-active --quiet "$unit"; then say "${green}✓${reset} $unit работает"; else warn "$unit не запущен: journalctl -u $unit"; problems=1; fi
done
if ufw status | grep -q '^Status: active'; then say "${green}✓${reset} UFW включён"; else warn 'UFW не включён'; problems=1; fi
jails=(sshd)
[[ $role == main ]] && jails+=(trusttunnel-admin)
for jail in "${jails[@]}"; do
  if fail2ban-client status "$jail" >/dev/null 2>&1; then say "${green}✓${reset} fail2ban: $jail"; else warn "fail2ban: правило $jail не активно"; fi
done
if [[ $role == node ]]; then
  if runuser -u trusttunnel-admin -- /opt/trusttunnel-admin/bin/trusttunnel-admin doctor >/dev/null 2>&1; then say "${green}✓${reset} нода получила пользователей от $main_host"; else warn "нода ещё не синхронизировалась с $main_host: journalctl -u trusttunnel-node"; problems=1; fi
elif curl -fsS -o /dev/null --max-time 10 --resolve "$domain:8443:127.0.0.1" "https://$domain:8443/healthz"; then say "${green}✓${reset} панель отвечает по HTTPS с действительным сертификатом"; else warn 'панель не ответила по HTTPS'; problems=1; fi
# A visitor without VPN credentials must see an ordinary website, not a proxy asking for a password.
decoy_status=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 --resolve "$domain:443:127.0.0.1" "https://$domain/" || true)
if [[ $decoy_status == 200 ]]; then say "${green}✓${reset} снаружи сервер выглядит как обычный сайт"; else warn "сайт-маска не ответила (код $decoy_status): сервер может выдавать себя как прокси"; problems=1; fi
say "${green}✓${reset} TCP: $(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || echo '?'), очередь: $(sysctl -n net.core.default_qdisc 2>/dev/null || echo '?')"

elapsed=$(( $(date +%s) - started ))
if [[ $role == node ]]; then
  unset panel_password
  cat <<EOF

${bold}================ Нода Netlane установлена ================${reset}
Нода:       «$server_name» ${server_country:+($server_country) }— $domain:443 (HTTP/2 по TCP и HTTP/3 по UDP)
Главный:    $main_host — пользователи и подписки управляются в его панели
Порты:      $ssh_port/tcp SSH, 80/tcp, 443/tcp+udp — остальное закрыто UFW
Сертификат: Let's Encrypt, продлевается сам
Маскировка: без VPN-пароля нода показывает обычный сайт
Копии:      /var/backups/trusttunnel каждую ночь в 04:00 MSK, хранятся 14.
Время:      $((elapsed / 60)) мин $((elapsed % 60)) с

Нода появится в разделе «Ноды» панели главного сервера и в подписках пользователей.
EOF
  if ((disable_passwords)); then
    say ''
    say "${yellow}Вход по SSH-паролю отключён. Не закрывайте эту сессию, пока не проверите вход по ключу в новом окне:${reset}"
    say "  ssh -p $ssh_port $owner@$domain"
  fi
  say '================================================================='
  ((problems == 0)) || { warn 'Есть предупреждения выше: проверьте их.'; exit 1; }
  exit 0
fi
cat <<EOF

${bold}================ Netlane установлен ================${reset}
Панель:  https://$domain:8443/${panel_allow:+ (только с $panel_allow)}
Логин:   admin
EOF
if ((password_reused)); then
  say 'Пароль: существующий, не изменён. Если он утрачен: /opt/trusttunnel-admin/bin/trusttunnel-admin passwd'
elif ((generated_password)); then
  printf 'Пароль:  %s%s%s\n\n%s\n' "$bold" "$panel_password" "$reset" 'Сохраните пароль сейчас: он показан один раз. Сменить — раздел «Безопасность» в панели.'
else
  say 'Пароль:  тот, что вы ввели.'
fi
unset panel_password
cat <<EOF

VPN:        $domain:443 (HTTP/2 по TCP и HTTP/3 по UDP)
Порты:      $ssh_port/tcp SSH, 80/tcp, 443/tcp+udp$( [[ -n $panel_allow || $panel_public == 1 ]] && echo ", 8443/tcp${panel_allow:+ (только с $panel_allow)}" || echo "; панель только локально" ) — остальное закрыто UFW
Защита:     fail2ban для SSH и панели (бан от 1 часа, растёт до недели)
Сертификат: Let's Encrypt, продлевается сам
Маскировка: без VPN-пароля сервер показывает обычный сайт; свой — положите в /var/lib/trusttunnel-admin/site
Копии:      /var/backups/trusttunnel каждую ночь в 04:00 MSK, хранятся 14.
            В них ключи шифрования: копируйте с сервера только в зашифрованном виде.
Время:      $((elapsed / 60)) мин $((elapsed % 60)) с

Дальше: войдите в панель, создайте пользователя, нажмите «Применить» и выдайте
профиль клиенту Netlane.
EOF
if ((disable_passwords)); then
  say ''
  say "${yellow}Вход по SSH-паролю отключён. Не закрывайте эту сессию, пока не проверите вход по ключу в новом окне:${reset}"
  say "  ssh -p $ssh_port $owner@$domain"
fi
say '==========================================================='
((problems == 0)) || { warn 'Есть предупреждения выше: проверьте их.'; exit 1; }
