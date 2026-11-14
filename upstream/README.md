# Управляемые изменения TrustTunnel

Источник: https://github.com/TrustTunnel/TrustTunnel

Закреплённая база: тег `v1.1.0`, commit `fab5b8353a19332f935fa30869307d37d4a898d1`. Лицензия upstream: Apache-2.0, копия рядом в `LICENSE.TrustTunnel`. Проект панели независим от разработчиков TrustTunnel.

Управляемый бинарник явно помечает версию суффиксом `-admin-preview.5`, чтобы не выдавать изменённую сборку за официальный релиз.

Патч `patches/0001-reload-credentials.patch` изменяет исходные файлы upstream:

- Добавляет замену реестра аутентификации во время работы.
- При SIGHUP читает TLS и пользователей; невалидные данные отклоняются без остановки VPN.
- Обновляет перечень пользователей в `/clients`.
- Сохраняет текущие счётчики соединений при обновлении списка, удалении и повторном добавлении тех же credentials.
- Повторно проверяет SNI-аутентификацию для новых запросов на уже открытом соединении.

Первый патч сам по себе не завершает передающие данные потоки и не применяет скорость. Второй патч `0002-managed-control-and-traffic.patch` добавляет Unix API, общий лимитер двух направлений, адресное закрытие транспортов и потоков и сохранение/восстановление политик. Третий патч `0003-managed-expiry.patch` добавляет сохраняемый срок доступа, отказ новых запросов и закрытие активных транспортов независимо от панели. Четвёртый патч `0004-durable-monthly-quota.patch` добавляет общую месячную квоту, журнал с предварительным резервированием и сохраняемую историю; контракт — в [QUOTAS.md](../docs/QUOTAS.md). Подробный контракт — в [CONTROL.md](../docs/CONTROL.md), срок и ограничения отката — в [EXPIRY.md](../docs/EXPIRY.md).

Параметры основного процесса, включая глобальные значения лимитов, не перечитываются. Аутентификация должна быть включена уже при запуске endpoint. Без managed-флагов сохраняется поведение первого патча; с ними пользователями управляет только локальный API, SIGHUP обновляет TLS.

Применение:

```sh
git clone https://github.com/TrustTunnel/TrustTunnel.git research/upstream
git -C research/upstream checkout fab5b8353a19332f935fa30869307d37d4a898d1
git -C research/upstream apply ../../upstream/patches/0001-reload-credentials.patch
git -C research/upstream apply ../../upstream/patches/0002-managed-control-and-traffic.patch
git -C research/upstream apply ../../upstream/patches/0003-managed-expiry.patch
git -C research/upstream apply ../../upstream/patches/0004-durable-monthly-quota.patch
git -C research/upstream apply ../../upstream/patches/0005-remove-managed-users.patch
git -C research/upstream apply ../../upstream/patches/0006-dependency-security-updates.patch
docker build -t tt-upstream-test -f deploy/ci/upstream.Dockerfile deploy/ci
docker run --rm -v "$PWD/research/upstream:/src" tt-upstream-test \
  bash -c 'cargo test --locked -p trusttunnel --lib && cargo test --locked -p trusttunnel --test managed && cargo test --locked -p trusttunnel_endpoint --bin trusttunnel_endpoint'
```

Нельзя применять патчи к произвольному master. В CI проверяются настоящие TLS H2 и QUIC H3 подключения и связка Go API → Unix RPC → endpoint. Матрица официальных мобильных/настольных клиентов, VM и полный установщик ещё впереди. Сборка с managed-флагами экспериментальная; не заменять ею работающий сервер до приёмки.

Патч `0005-remove-managed-users.patch` добавляет capability `remove_user` и версионированную команду удаления. Сессии и credentials отзываются после сохранения managed state; журнал расхода сохраняется. Повтор по отсутствующему ID не удаляет нового пользователя с тем же именем.

Патч `0006-dependency-security-updates.patch` меняет `Cargo.lock` и одну строку `lib/src/icmp_forwarder.rs` (`AsyncFd::register` вместо устаревшего небезопасного `AsyncFd::new`, RUSTSEC-2026-0386): h2 0.4.20, rustls 0.23.46, rustls-webpki 0.103.17, webpki-roots 1.0.12, tokio 1.54.1, rustix 1.1.6, quinn-proto 0.11.19, quinn-udp 0.5.16 — исправления известных уязвимостей (RustSec). Остаётся h2 0.3.27 (прямая зависимость upstream, исправления для ветки 0.3 нет; уязвимость низкой важности) — CI проверяет остальное `cargo audit`.
