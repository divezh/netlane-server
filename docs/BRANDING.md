# Брендинг Netlane

Ребрендинг завершён: **Netlane** — новое имя продукта, панели и Windows-клиента.
Старые имена остаются только там, где они нужны для совместимости с уже
работающими установками, или в исторических документах.

## Новые имена

| Что | Новое имя |
| --- | --- |
| Веб-панель | **Netlane Console** |
| Go-модуль | `github.com/divezh/netlane-server` |
| Скрипты установки | `scripts/netlane-setup.sh`, `scripts/netlane-update.sh`, `scripts/netlane-remote-install.sh` |
| Переменные окружения | `NETLANE_*` |
| Команда обновления | `netlane-update` |

Упоминания **TrustTunnel** остаются как название стороннего VPN-движка, а также
в его лицензиях и атрибуции. Исходники и патчи upstream TrustTunnel не менялись.

## Иконка

- `assets/netlane-icon.svg` — векторный оригинал (вариант Lane N).
- `assets/netlane-mark-mono.svg` — монохромный знак без фона.
- `assets/netlane-icon.png` — растр 512×512, мастер для генератора.

## Совместимость с прежними именами

При первом запуске новые версии читают данные прежних имён и продолжают
работать без ручной миграции.

### Сервер и обновлятор

| Прежнее имя | Как обрабатывается |
| --- | --- |
| Службы `trusttunnel-admin`, `trusttunnel-node`, `trusttunnel-managed` | Не переименовывались: это рабочие systemd-службы |
| `/etc/trusttunnel*`, `/var/lib/trusttunnel*`, сокет `/run/trusttunnel` | Не переименовывались: существующие данные, права и конфигурация |
| Переменные `WTT_*` | Принимаются мастером установки и обновлятором как псевдонимы `NETLANE_*` |
| Состояние установки `/var/lib/wtt-install` | Используется, если это незавершённая установка прежнего имени |
| Команда `wtt-update` | Символическая ссылка на `netlane-update` |
| Токен `/etc/wtt-update/github-token` | Читается, пока не сохранён новый токен в `/etc/netlane-update/github-token` |
| Теги `wintrusttunnel-X.Y.Z` | Остались в архиве прежних выпусков; новые теги — `netlane-X.Y.Z` |
| Каталог отката `/var/lib/wtt-update/previous` | Используется, если нового каталога ещё нет |
| Репозиторий `divezh/WinTrustTunnel` | Остаётся только архивом прежних выпусков; проект живёт в `divezh/netlane-server` |

### Что не переименовывается

- Выпуски клиентов и их примечания живут в `divezh/netlane-windows` и `divezh/netlane-macos`.
- Репозиторий `divezh/WinTrustTunnel` — закрытый архив прежних выпусков; новые
  релизы серверной части выходят в `divezh/netlane-server`.
- Исходники, лицензии и атрибуция upstream TrustTunnel.

## Проверка

В актуальном коде старые имена допустимы только в перечисленных выше местах
совместимости и в исторических документах. Для проверки:

```bash
grep -rniE 'wintrusttunnel|wtt|WinTrustTunnel' . \
  --exclude-dir=.git --exclude-dir=releases --exclude-dir=archive
```

Каждое совпадение должно относиться к таблицам совместимости, slug репозитория
или upstream TrustTunnel.
