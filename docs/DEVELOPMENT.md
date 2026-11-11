# Локальная разработка

## Стек и структура

Go-панель использует SQLite и встроенные HTML/CSS/JavaScript. VPN-движки закреплены на конкретных исходниках и патчах. [Карта проекта](ARCHITECTURE.md).

## Панель и API

Нужна версия Go из `go.mod`. Для полного набора Linux-проверок используйте Linux/WSL или локальный Docker. На Windows отдельные Unix-проверки прав отличаются.

```bash
go test -race ./...
go vet ./...
mkdir -p artifacts runtime
go build -o artifacts/trusttunnel-admin ./cmd/trusttunnel-admin
./artifacts/trusttunnel-admin setup --config ./runtime/config.json --data-dir ./runtime
./artifacts/trusttunnel-admin serve --config ./runtime/config.json
```

Этот сценарий запускает панель разработки. Без управляемого endpoint она не подтверждает применение VPN-доступов. Не используйте его вместо VPS-установщика. `runtime/` и `artifacts/` исключены из Git.

## Полный Linux-комплект

Комплект собирается локально, без CI. Путей два, выбор зависит от того, менялся ли endpoint.

**Без Docker** — когда управляемый endpoint уже есть. Он пришпилен к upstream и меняется редко, поэтому для правки панели пересобирается только она:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/build-dev-bundle.ps1 -Version 0.5.9
```

Скрипт берёт endpoint из `artifacts/trusttunnel_endpoint`, а если файла нет — из последнего релиза; собирает панель под linux/amd64; упаковывает `artifacts/trusttunnel-dev.tar.gz` с теми же записями, порядком, правами и переводами строк, что и bash-скрипт. Пакет `.deb` этот путь не собирает: в релиз он не входит. Нужны Go и Python 3 — сборка идёт на Windows, GNU tar и `dpkg-deb` не требуются.

**С Docker** — когда нужен и сам endpoint, например после смены закреплённого upstream:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/build-local.ps1 -Bundle
```

На Linux то же самое делают `bash scripts/build-dev-bundle.sh` (панель и комплект).

Результат в обоих случаях: `artifacts/trusttunnel-dev.tar.gz`, `.sha256` и запись ревизии в `SOURCE_COMMIT`. Обычный upstream endpoint не заменяет управляемую сборку.

## Проверки

| Изменение | Проверка |
| --- | --- |
| Go API, хранилище, синхронизация | `go test -race ./...`, `go vet ./...` |
| Дашборды и автообновление | `node scripts/test-web-presence.mjs` |
| Установщик и восстановление | `python scripts/test-dev-install.py --panel artifacts/trusttunnel-admin --endpoint artifacts/trusttunnel_endpoint` |
| Ранние проверки setup/install | `python3 scripts/test-setup-startup.py` в одноразовом Linux-контейнере от root |
| Shell | ShellCheck изменённых скриптов |

Браузерному тесту нужны `playwright-core` и Edge. Путь к внешней установке пакета можно задать через `TT_PLAYWRIGHT_MODULE`; API подменяется локальными тестовыми ответами. Подробности — в [WEB_QA.md](WEB_QA.md).

Тест полного установщика использует локальные привилегированные одноразовые контейнеры с systemd, без портов/каталогов production. Для Ubuntu 26.04 задайте `--ubuntu-version 26.04 --image netlane-installer-ubuntu26`. [Описание стенда](INSTALLER-RECOVERY.md).

`scripts/install.sh` и `.deb` — прежний отдельный preview-стенд панели, не основной установщик VPN; в релизы пакет не кладётся, собрать его можно только вручную (`bash scripts/build-deb.sh <версия>` на Linux). `scripts/test-installer.sh` проверяет этот стенд и распознавание ОС. Основной VPS-комплект использует `scripts/netlane-setup.sh` и `deploy/dev/install.sh`.

## Публикация

Сборки и проверки выполняются локально. Workflows не запускаются на push; ручной запуск Actions расходует лимиты аккаунта. Основной путь — ручная загрузка проверенных архивов и `.sha256` в Releases. Заголовок релиза — `Netlane <версия> - Server`: по нему серверный выпуск отличают от выпусков Windows-клиента в репозитории приложения. Примечания выпусков клиента находятся в репозитории приложения; теги опубликованных версий не перемещаются.
