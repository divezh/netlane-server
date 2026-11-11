<p align="center"><img src="assets/netlane-icon.png" width="88" alt="Netlane"></p>

# Netlane Server

Закрытый VPN на базе [TrustTunnel](https://github.com/TrustTunnel/TrustTunnel): веб-панель, пользователи и ноды в нескольких локациях.

[Последний релиз](https://github.com/divezh/netlane-server/releases/latest) · [Документация](docs/README.md) · Клиенты: [Windows](https://github.com/divezh/netlane-windows) · [macOS](https://github.com/divezh/netlane-macos)

![Админ-панель Netlane — демонстрационные данные](assets/screenshots/admin-overview.png)

## Возможности

- Пользователи, сроки доступа, лимиты скорости и трафика, группы локаций.
- Ноды и одна подписка на все локации; общие правила маршрутизации и DNS.
- Дашборды: онлайн, трафик, CPU, память, диск.
- HTTP/2, HTTP/3 (QUIC), Anti-DPI; HTTPS, 2FA, Fail2ban, резервные копии.
- Обновление кнопкой в панели из последнего релиза этого репозитория.

Go + SQLite + systemd; Docker и Node.js не нужны.

## Установка

Чистый VPS x86_64 (Ubuntu 24.04 LTS, Ubuntu 26.04 или Debian 13), root, домен с A-записью на сервер, от 1 ГБ RAM. [Подробнее](docs/INSTALLATION.md).

```bash
cd /root
curl -fsSLO https://github.com/divezh/netlane-server/releases/latest/download/trusttunnel-dev.tar.gz
curl -fsSLO https://github.com/divezh/netlane-server/releases/latest/download/trusttunnel-dev.tar.gz.sha256
sha256sum -c trusttunnel-dev.tar.gz.sha256 &&
mkdir -p netlane-install && tar -xzf trusttunnel-dev.tar.gz -C netlane-install &&
bash netlane-install/setup.sh
```

Мастер выдаст адрес панели и данные входа. Включите 2FA и создайте пользователя.

## Полезные команды

```bash
sudo netlane-update                 # обновить до последнего релиза (или кнопкой в панели)
sudo netlane-update --check         # только проверить
systemctl status trusttunnel-admin trusttunnel-managed   # состояние панели и VPN
journalctl -u trusttunnel-admin -u trusttunnel-update -n 100   # журнал панели и обновлений
bash netlane-install/setup.sh --resume           # продолжить прерванную установку
```

`setup.sh` для обновления не используйте. [Обновления](docs/UPDATES.md) · [Восстановление установки](docs/INSTALLER-RECOVERY.md) · [Ноды](docs/NODES.md)

## Разработка

[Сборка и тесты](docs/DEVELOPMENT.md) · [Архитектура](docs/ARCHITECTURE.md) · [Внесение изменений](CONTRIBUTING.md) · [Безопасность](SECURITY.md)

Проект независим от разработчиков TrustTunnel.
