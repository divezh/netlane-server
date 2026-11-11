#!/usr/bin/env bash
# Netlane server updater: finds the latest release of divezh/netlane-server, installs it and restarts
# the services. Works on the main server and on nodes. Before changing anything it makes a
# backup; if the services do not come back healthy, the previous programs are restored.
#
#   netlane-update                 check and update (asks before installing)
#   netlane-update --check         only report whether a new version is out
#   netlane-update --yes           update without the question
#   netlane-update --requested     update only when the console left a request (systemd timer)
#   netlane-update --file F.tar.gz install a downloaded trusttunnel-dev.tar.gz (F.tar.gz.sha256 next to it)
#   netlane-update --set-token     save a GitHub token (optional: raises GitHub's limit of 60 requests an hour)
#
# Installed by install.sh as /usr/local/sbin/netlane-update.
set -euo pipefail
umask 077

REPO=${NETLANE_REPO:-divezh/netlane-server}
API=${NETLANE_API:-${WTT_API:-https://api.github.com}}
TOKEN_FILE=${NETLANE_TOKEN_FILE:-${WTT_TOKEN_FILE:-/etc/netlane-update/github-token}}
# A token saved by the former command name is read until a new one is set.
LEGACY_TOKEN_FILE=/etc/wtt-update/github-token
# The console leaves a request here; the systemd timer below runs this script with --requested.
REQUESTED=/var/lib/trusttunnel-admin/update-requested
# The updater reports its step here; the console reads it for the progress bar.
STATUS=/var/lib/trusttunnel-admin/update-status.json
ADMIN=/opt/trusttunnel-admin/bin/trusttunnel-admin
ENDPOINT=/opt/trusttunnel/bin/trusttunnel_endpoint
BACKUP=/opt/trusttunnel-admin/bin/trusttunnel-backup
MARKER=/opt/trusttunnel-admin/SOURCE_COMMIT

bold=$'\e[1m' red=$'\e[31m' green=$'\e[32m' yellow=$'\e[33m' reset=$'\e[0m'
[[ -t 1 ]] || { bold='' red='' green='' yellow='' reset=''; }
say() { printf '%s\n' "$*"; }
warn() { printf '%s! %s%s\n' "$yellow" "$*" "$reset" >&2; }
# The reason of a failed run goes to the console too, not only to the terminal or the journal.
fail_reason=''
die() { fail_reason="$*"; printf '%sОшибка: %s%s\n' "$red" "$*" "$reset" >&2; exit 1; }

check_only=0 assume_yes=0 file='' set_token=0 requested=0
while (($#)); do
  case $1 in
    --check) check_only=1 ;;
    --yes|-y) assume_yes=1 ;;
    --file) file=${2:-}; shift ;;
    --file=*) file=${1#--file=} ;;
    --channel) ;; # accepted for old timers and habits: there is only one source now
    --requested) requested=1 ;;
    --set-token) set_token=1 ;;
    -h|--help) sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "неизвестный параметр: $1 (см. netlane-update --help)" ;;
  esac
  shift
done

# Megabytes with one decimal for the progress line: «4,2».
mb() { awk -v b="$1" 'BEGIN{printf "%.1f", b/1048576}' | tr . ,; }

# The update timer lets the unprivileged console ask for an update. It is reinstalled when
# missing, so a server that came from an older updater receives it on any later run.
# status PERCENT STAGE MESSAGE [DETAIL] — the console shows this as the progress bar and a list
# of steps. A repeated STAGE updates its step (download progress); a new one starts a step.
# Failing to write the report never stops the update.
step_stages=() step_messages=() step_details=() step_times=()
status() {
  local percent=$1 stage=$2 message=$3 detail=${4:-} now
  now=$(date +%s)
  if [[ $stage != failed ]]; then
    local last=$(( ${#step_stages[@]} - 1 ))
    if (( last >= 0 )) && [[ ${step_stages[last]} == "$stage" ]]; then
      step_messages[last]=$message step_details[last]=$detail
    else
      step_stages+=("$stage") step_messages+=("$message") step_details+=("$detail") step_times+=("$now")
    fi
  fi
  {
    printf '%s\x1f%s\x1f%s\x1f%s\x1f%s\x1f%s\n' "$stage" "$percent" "$message" "$detail" "${version:-}" "$now"
    local i
    for ((i=0; i<${#step_stages[@]}; i++)); do
      printf '%s\x1f%s\x1f%s\x1f%s\n' "${step_stages[i]}" "${step_messages[i]}" "${step_details[i]}" "${step_times[i]}"
    done
  } | python3 -c '
import json, sys
lines = sys.stdin.read().split("\n")
stage, percent, message, detail, version, at = lines[0].split("\x1f")
steps = []
for line in lines[1:]:
    if line:
        s, m, d, t = line.split("\x1f")
        steps.append({"stage": s, "message": m, "detail": d, "at": int(t)})
report = {"stage": stage, "percent": None if percent == "null" else int(percent), "message": message,
          "detail": detail, "version": version, "at": int(at), "steps": steps}
if stage == "failed" and steps:
    report["failed_stage"] = steps[-1]["stage"]
print(json.dumps(report, ensure_ascii=False))' >"$STATUS.tmp" 2>/dev/null && mv -f "$STATUS.tmp" "$STATUS" 2>/dev/null || return 0
  chmod 0644 "$STATUS" 2>/dev/null || true
}

# The console asks for an update by leaving $REQUESTED. A path unit starts the updater as
# soon as the file appears; a calendar timer every two minutes is the fallback. The former
# timer (OnUnitActiveSec) could be left without a next run, and then a request waited forever.
put_unit() { # put_unit FILE — content on stdin; returns 0 when the file changed
  local file=$1 new
  new=$(cat)
  [[ -f $file && $(cat "$file") == "$new" ]] && return 1
  printf '%s\n' "$new" >"$file.tmp" && chmod 0644 "$file.tmp" && mv -f "$file.tmp" "$file"
}
install_update_timer() {
  command -v systemctl >/dev/null || return 0
  local changed=0
  put_unit /etc/systemd/system/trusttunnel-update.service <<'UNIT' && changed=1
[Unit]
Description=Netlane console update request
After=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/netlane-update --requested
UNIT
  put_unit /etc/systemd/system/trusttunnel-update.path <<'UNIT' && changed=1
[Unit]
Description=Start a console update as soon as it is requested

[Path]
PathExists=/var/lib/trusttunnel-admin/update-requested
Unit=trusttunnel-update.service

[Install]
WantedBy=paths.target
UNIT
  put_unit /etc/systemd/system/trusttunnel-update.timer <<'UNIT' && changed=1
[Unit]
Description=Read a console update request every two minutes (fallback for the path unit)

[Timer]
OnCalendar=*:0/2
AccuracySec=15s
Unit=trusttunnel-update.service

[Install]
WantedBy=timers.target
UNIT
  ((changed)) && { systemctl daemon-reload || true; }
  # A timer rewritten while it runs keeps its old schedule until it is restarted.
  ((changed)) && systemctl restart trusttunnel-update.timer >/dev/null 2>&1 || true
  systemctl enable --now trusttunnel-update.path trusttunnel-update.timer >/dev/null 2>&1 ||
    warn 'запуск обновления из панели не включился; обновляйте по SSH'
}
((EUID == 0)) || die 'запустите от root: sudo netlane-update'
install_update_timer

if ((requested)); then
  # The console cannot install anything: it only leaves a request, and this run reads it.
  [[ -f $REQUESTED ]] || exit 0
  requested_at=$(python3 -c 'import json,sys;print(int(json.load(open(sys.argv[1])).get("requested_at",0)))' "$REQUESTED" 2>/dev/null || echo 0)
  rm -f -- "$REQUESTED"
  assume_yes=1
  waited=$(( $(date +%s) - requested_at ))
  status 5 accepted 'Запрос из панели принят' "$( ((requested_at > 0 && waited >= 0 && waited < 86400)) && echo "через $waited с после нажатия" )"
fi

if ((set_token)); then
  say 'Токен GitHub с доступом только на чтение к репозиторию (Fine-grained token → Repository access:'
  say "только $REPO → Permissions: Contents — Read-only). Вставьте токен и нажмите Enter:"
  read -r -s token </dev/tty; printf '\n'
  [[ $token =~ ^[A-Za-z0-9_]{20,255}$ ]] || die 'это не похоже на токен GitHub'
  install -d -m 0700 "$(dirname "$TOKEN_FILE")"
  printf '%s\n' "$token" >"$TOKEN_FILE.tmp" && chmod 0600 "$TOKEN_FILE.tmp" && mv "$TOKEN_FILE.tmp" "$TOKEN_FILE"
  unset token
  say "${green}Токен сохранён в $TOKEN_FILE (только root).${reset}"
  exit 0
fi

[[ -x $ADMIN ]] || die "не найден $ADMIN: на этом сервере Netlane не установлен (для установки — setup.sh)"
if [[ -f /etc/systemd/system/trusttunnel-node.service ]]; then role=node service=trusttunnel-node
else role=main service=trusttunnel-admin; fi

# The country database for client flags. install.sh has always installed it, but an
# updated server never received it — and without the database the panel draws no flags
# for anyone, because geoip-update and its timer were missing too.
geoip_file=${NETLANE_GEOIP_FILE:-}
if [[ -z $geoip_file ]]; then
  geoip_file=$(python3 -c 'import json;print(json.load(open("/etc/trusttunnel-admin/config.json")).get("geoip_path",""))' 2>/dev/null || true)
  geoip_file=${geoip_file:-/var/lib/trusttunnel-admin/geoip.mmdb}
fi
export NETLANE_GEOIP_FILE=$geoip_file
geoip_ready() { [[ -s $geoip_file ]]; }

# A release build knows its version; a development build only its commit.
installed=$("$ADMIN" version 2>/dev/null || true); installed=${installed#dev-}
if [[ ! $installed =~ ^[0-9]+\.[0-9]+\.[0-9]+$ && -s $MARKER ]]; then
  installed=$(tr -dc '0-9a-f' <"$MARKER")
fi
[[ $installed =~ ^[0-9a-f]{7,40}$ || $installed =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || installed=unknown

# Stage beside the installed binary: /tmp may be mounted noexec on hardened VPSes.
# mktemp creates a root-only directory; the application cannot replace its contents.
work=$(mktemp -d /opt/trusttunnel-admin/.update.XXXXXX)
# A failed run must leave a readable reason: the console shows it instead of a stuck bar.
cleanup() {
  local code=$?
  if ((code != 0)); then
    status null failed "${fail_reason:-Обновление прервано: смотрите journalctl -u trusttunnel-update -n 50}"
  fi
  [[ -n ${work:-} ]] && rm -rf -- "$work"
}
trap cleanup EXIT

# GitHub API; a saved token is optional and only raises the hourly request limit.
token=${NETLANE_GITHUB_TOKEN:-}
[[ -z $token && -r $TOKEN_FILE ]] && token=$(tr -d '[:space:]' <"$TOKEN_FILE")
[[ -z $token && -r $LEGACY_TOKEN_FILE ]] && token=$(tr -d '[:space:]' <"$LEGACY_TOKEN_FILE")
api() { # api PATH [accept] [output]
  local args=(-fsSL --proto '=https' --max-time 60 -H "Accept: ${2:-application/vnd.github+json}" -H 'X-GitHub-Api-Version: 2022-11-28')
  [[ $API == https://* ]] || args=(-fsSL --max-time 60 -H "Accept: ${2:-application/vnd.github+json}")
  [[ -n $token ]] && args+=(-H "Authorization: Bearer $token")
  curl "${args[@]}" ${3:+-o "$3"} "$API$1"
}

if [[ -n $file ]]; then
  [[ -f $file && -f $file.sha256 ]] || die "нужны $file и $file.sha256"
  cp -- "$file" "$work/trusttunnel-dev.tar.gz"
  cp -- "$file.sha256" "$work/trusttunnel-dev.tar.gz.sha256"
  tag=$(basename "$file")
else
  # The only source: the latest published release of the public repository.
  if ! api "/repos/$REPO/releases/latest" '' "$work/release.json" 2>"$work/err"; then
    die "GitHub не отдал последний релиз $REPO: $(head -c 300 "$work/err"). Можно скачать trusttunnel-dev.tar.gz и .sha256 вручную и запустить netlane-update --file trusttunnel-dev.tar.gz"
  fi
  read -r tag direct_url direct_sum sum_url notes_url < <(python3 - "$work/release.json" "$REPO" <<'PY'
import json, sys
r = json.load(open(sys.argv[1])); repo = sys.argv[2]
assets = {a.get("name"): a for a in r.get("assets", [])}
bundle = assets.get("trusttunnel-dev.tar.gz", {}); summ = assets.get("trusttunnel-dev.tar.gz.sha256", {})
digest = (bundle.get("digest") or "").lower()
print(r.get("tag_name", "-"), bundle.get("browser_download_url") or "-", digest[7:] if digest.startswith("sha256:") else "-",
      summ.get("browser_download_url") or "-", r.get("html_url") or "-")
PY
) || die 'GitHub вернул неожиданный ответ о релизе'
  [[ $direct_sum == - ]] && direct_sum=''
  [[ $notes_url == - ]] && notes_url=''
  [[ $direct_url == "https://github.com/$REPO/releases/download/"* ]] || die "в релизе $tag нет trusttunnel-dev.tar.gz"
  version=${tag#netlane-}; version=${version#v}
  [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "тег $tag не похож на версию"
  status 10 channel "Последний релиз: $version" "установлена $([[ $installed == unknown ]] && echo 'неизвестная версия' || { [[ $installed =~ ^[0-9a-f]{7,40}$ ]] && echo "сборка ${installed:0:7}" || echo "$installed"; })"
  if [[ $installed =~ ^[0-9]+\.[0-9]+\.[0-9]+$ && $installed == "$version" ]]; then
    if geoip_ready; then
      say "${green}Установлена последняя версия $version.${reset}"
      status 100 'done' "Уже установлена последняя версия $version"
      exit 0
    fi
    say "Версия $version уже установлена, но базы стран нет: доустанавливаю её."
  fi
  say "Доступна версия ${bold}$version${reset}; установлена: $([[ $installed == unknown ]] && echo 'неизвестна' || echo "$installed")."
  say "Что нового: ${notes_url:-https://github.com/$REPO/releases/tag/$tag}"
  if ((check_only)); then exit 10; fi
  if ((!assume_yes)); then
    [[ -t 0 ]] || die 'нет терминала для вопроса: запустите с --yes'
    read -r -p "Обновить сейчас? Сервис VPN перезапустится, соединения клиентов прервутся на несколько секунд [y/N]: " answer
    [[ ${answer,,} =~ ^(y|yes|д|да)$ ]] || { say 'Обновление отменено.'; exit 0; }
  fi
  status 15 download 'Скачиваю комплект' 'подключаюсь к github.com'
  say 'Скачиваю…'
  # GitHub redirects release downloads to its file host; the checksum below decides, not the host.
  total=$(curl -fsSIL --proto '=https' --max-time 20 "$direct_url" 2>/dev/null | tr -d '\r' | awk 'tolower($1)=="content-length:"{v=$2} END{print v+0}') || total=0
  [[ $total =~ ^[0-9]+$ ]] || total=0
  started=$(date +%s)
  curl -fsSL --proto '=https' --max-time 600 "$direct_url" -o "$work/trusttunnel-dev.tar.gz" 2>"$work/download.err" &
  download=$!
  while kill -0 "$download" 2>/dev/null; do
    got=$(stat -c %s "$work/trusttunnel-dev.tar.gz" 2>/dev/null || echo 0)
    if ((total > 0)); then
      status $(( 15 + 35 * got / total )) download 'Скачиваю комплект' "$(mb "$got") из $(mb "$total") МБ"
    else
      status 30 download 'Скачиваю комплект' "$(mb "$got") МБ"
    fi
    sleep 1
  done
  wait "$download" || die "не удалось скачать trusttunnel-dev.tar.gz: $(head -c 200 "$work/download.err")"
  got=$(stat -c %s "$work/trusttunnel-dev.tar.gz")
  status 50 download 'Комплект скачан' "$(mb "$got") МБ за $(( $(date +%s) - started )) с"
  if [[ $direct_sum =~ ^[0-9a-f]{64}$ ]]; then
    printf '%s  trusttunnel-dev.tar.gz\n' "$direct_sum" >"$work/trusttunnel-dev.tar.gz.sha256"
  else
    [[ $sum_url == "https://github.com/$REPO/releases/download/"* ]] || die "у релиза $tag нет контрольной суммы"
    curl -fsSL --proto '=https' --max-time 60 "$sum_url" -o "$work/trusttunnel-dev.tar.gz.sha256" || die 'не удалось скачать контрольную сумму'
  fi
fi

status 52 verify 'Проверяю контрольную сумму' 'SHA-256 из релиза'
# Compare the hash itself: a renamed download keeps the original name inside the .sha256 file.
expected=$(awk 'NR==1{print $1}' "$work/trusttunnel-dev.tar.gz.sha256")
actual=$(sha256sum "$work/trusttunnel-dev.tar.gz" | awk '{print $1}')
[[ $expected =~ ^[0-9a-f]{64}$ && $expected == "$actual" ]] || die "контрольная сумма не совпала: ожидалась ${expected:0:12}…, получена ${actual:0:12}…"
status 55 verify 'Контрольная сумма совпала' "SHA-256 ${actual:0:16}…"
status 58 unpack 'Распаковываю и проверяю программы'
mkdir "$work/new"
tar -xzf "$work/trusttunnel-dev.tar.gz" -C "$work/new"
for f in bin/trusttunnel-admin bin/trusttunnel_endpoint backup/trusttunnel-backup SOURCE_COMMIT; do
  [[ -f $work/new/$f ]] || die "в комплекте нет $f"
done
new_commit=$(tr -dc '0-9a-f' <"$work/new/SOURCE_COMMIT")
new_version=$("$work/new/bin/trusttunnel-admin" version 2>/dev/null) || die 'новая программа панели не запускается на этом сервере'
status 62 unpack 'Программы проверены' "панель $new_version, сборка ${new_commit:0:7}"

status 65 backup 'Делаю резервную копию' 'база, настройки и сертификаты'
say 'Резервная копия…'
"$BACKUP" >/dev/null || die 'резервная копия не создана, обновление остановлено'
backup_name=$(find /var/backups/trusttunnel -maxdepth 1 -type f -name 'backup-*.tar.gz' -printf '%T@ %f\n' | sort -nr | sed -n '1s/^[^ ]* //p')
say "  $backup_name"
status 70 backup 'Резервная копия готова' "/var/backups/trusttunnel/$backup_name"

prev=/var/lib/netlane-update/previous
# Rollback copies of the former name stay reachable.
[[ -d /var/lib/wtt-update/previous && ! -e $prev ]] && prev=/var/lib/wtt-update/previous
install -d -m 0700 "$prev"
cp -p "$ADMIN" "$prev/trusttunnel-admin"
cp -p "$ENDPOINT" "$prev/trusttunnel_endpoint"
cp -p "$BACKUP" "$prev/trusttunnel-backup"

endpoint_changed=0
cmp -s "$work/new/bin/trusttunnel_endpoint" "$ENDPOINT" || endpoint_changed=1

healthy() {
  local domain
  for ((i=0; i<30; i++)); do
    if systemctl is-active --quiet trusttunnel-managed "$service"; then
      if [[ $role == main ]]; then
        domain=$(python3 -c 'import json;print(json.load(open("/etc/trusttunnel-admin/config.json"))["domain"])')
        curl -fs -o /dev/null --max-time 5 --resolve "$domain:8443:127.0.0.1" "https://$domain:8443/healthz" && return 0
      else
        runuser -u trusttunnel-admin -- "$ADMIN" doctor >/dev/null 2>&1 && return 0
      fi
    fi
    sleep 2
  done
  return 1
}

restart() {
  ((endpoint_changed)) && systemctl restart trusttunnel-managed
  systemctl restart "$service"
}

status 75 install 'Устанавливаю программы' "$( ((endpoint_changed)) && echo 'панель и движок VPN' || echo 'панель; движок VPN не изменился')"
say 'Устанавливаю…'
install -m 0755 "$work/new/bin/trusttunnel-admin" "$ADMIN"
install -m 0755 "$work/new/bin/trusttunnel_endpoint" "$ENDPOINT"
install -m 0755 "$work/new/backup/trusttunnel-backup" "$BACKUP"
status 82 restart 'Перезапускаю службы' "$( ((endpoint_changed)) && echo 'VPN и панель: клиенты переподключатся за несколько секунд' || echo 'только панель: VPN-соединения не прерываются')"
if ! { restart && status 88 health 'Проверяю, что службы отвечают' "$( [[ $role == main ]] && echo 'панель: /healthz на порту 8443' || echo 'нода: netlane doctor')" && healthy; }; then
  warn 'после обновления сервисы не поднялись — возвращаю прежнюю версию'
  status 90 health 'Службы не поднялись — возвращаю прежнюю версию' 'откат на программы из /var/lib/netlane-update/previous'
  install -m 0755 "$prev/trusttunnel-admin" "$ADMIN"
  install -m 0755 "$prev/trusttunnel_endpoint" "$ENDPOINT"
  install -m 0755 "$prev/trusttunnel-backup" "$BACKUP"
  restart || true
  if healthy; then die "обновление не удалось, работает прежняя версия. Журнал: journalctl -u $service -u trusttunnel-managed -n 100"; fi
  die "обновление не удалось, и прежняя версия тоже не поднялась. Резервная копия: /var/backups/trusttunnel. Журнал: journalctl -u $service -u trusttunnel-managed -n 100"
fi
printf '%s\n' "$new_commit" >"$MARKER"
status 94 finish 'Службы работают, обновляю служебные файлы' 'fail2ban, база стран, программа обновления'
# Supporting files that come with the release.
# Update only our description; retain any administrator's backup schedule/overrides.
if [[ -f /etc/systemd/system/trusttunnel-backup.timer ]]; then
  sed -i '/^Description=Daily Netlane backup at /c\Description=Daily Netlane backup at 04:00 MSK (01:00 UTC)' /etc/systemd/system/trusttunnel-backup.timer
  systemctl daemon-reload
fi
if [[ $role == main && -d $work/new/fail2ban && -d /etc/fail2ban/filter.d ]]; then
  install -m 0644 "$work/new/fail2ban/trusttunnel-admin.filter.conf" /etc/fail2ban/filter.d/trusttunnel-admin.conf
  install -m 0644 "$work/new/fail2ban/trusttunnel-admin.jail.local" /etc/fail2ban/jail.d/trusttunnel-admin.local
  systemctl try-reload-or-restart fail2ban >/dev/null 2>&1 || true
fi
if [[ -f $work/new/geoip-update ]]; then
  install -m 0750 "$work/new/geoip-update" /usr/local/sbin/geoip-update
  if [[ -f $work/new/geoip/trusttunnel-geoip.service && -f $work/new/geoip/trusttunnel-geoip.timer ]]; then
    install -m 0644 "$work/new/geoip/trusttunnel-geoip.service" /etc/systemd/system/trusttunnel-geoip.service
    install -m 0644 "$work/new/geoip/trusttunnel-geoip.timer" /etc/systemd/system/trusttunnel-geoip.timer
    systemctl daemon-reload
    systemctl enable --now trusttunnel-geoip.timer >/dev/null 2>&1 || true
  fi
  if ! geoip_ready; then
    say 'Скачиваю базу стран для флагов клиентов…'
    systemctl start trusttunnel-geoip.service >/dev/null 2>&1 ||
      warn "база стран не скачана: флаги не появятся, пока не сработает geoip-update (файл $geoip_file)"
  fi
fi
if [[ -f $work/new/update.sh ]]; then
  install -m 0755 "$work/new/update.sh" /usr/local/sbin/netlane-update
  # Keep the former command name working.
  ln -sf netlane-update /usr/local/sbin/wtt-update
  install_update_timer

fi
published=${tag#netlane-}; published=${published#wintrusttunnel-}
status 100 'done' "Готово: $published установлена" "$( ((endpoint_changed)) && echo 'панель и движок VPN обновлены' || echo 'панель обновлена, VPN работал без перерыва')"
say "${green}Готово: $published (${new_commit:0:7}) установлена, сервисы работают.${reset}"
((endpoint_changed)) && say 'Движок VPN обновлён: клиенты переподключились автоматически.'
exit 0
