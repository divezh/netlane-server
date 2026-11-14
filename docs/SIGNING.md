# Подпись релизов

С версии 0.6.1 серверы ставят только подписанные релизы. Подпись делает владелец своим ключом,
которого нет на GitHub: даже с доступом к аккаунту, токену или Actions нельзя выпустить
комплект, который примут установленные серверы, ноды и приложения Netlane.

## Ключ

ECDSA P-256, подпись SHA-256 в формате DER (`openssl dgst -sha256 -sign`). Открытый ключ —
[`netlane-release.pub`](../netlane-release.pub), он же зашит в `netlane-update`, `setup.sh`,
команду ноды в панели и в клиенты Windows и macOS.

```
-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEtcoVFvdfswZyFlDqvHPR4ZS7wxQL
q7iwNHtv+nx0GBdMsfRqNxJEEuOmJQhX8K87n59fJ8x5K7SFVPT3EbPWdg==
-----END PUBLIC KEY-----
```

SHA-256 открытого ключа (DER): `8c6ed42d682f9f6a48e7745b2a7a1935feedb08600379546f544ace67a8a8136`.

Секретный ключ хранится у владельца вне GitHub и облаков, с резервной копией офлайн. Если он
потерян, установленные серверы и приложения не примут ни одного нового релиза: их придётся
переустановить с новым ключом вручную.

## Выпуск

1. Actions → Release (сервер) или тег `vX.Y.Z` (клиенты) собирает комплект и создаёт
   **черновик** релиза. Черновик никто не видит: обновлятор и приложения берут только
   опубликованный последний релиз.
2. Владелец скачивает файл из черновика, подписывает и прикладывает подпись:

   ```sh
   openssl dgst -sha256 -sign netlane-release.key -out trusttunnel-dev.tar.gz.sig trusttunnel-dev.tar.gz
   openssl dgst -sha256 -verify netlane-release.pub -signature trusttunnel-dev.tar.gz.sig trusttunnel-dev.tar.gz
   gh release upload netlane-X.Y.Z trusttunnel-dev.tar.gz.sig -R divezh/netlane-server
   gh release edit netlane-X.Y.Z --draft=false --latest -R divezh/netlane-server
   ```

   То же делает `scripts/sign-release.sh REPO TAG ФАЙЛ КЛЮЧ` (нужны `gh` и `openssl`).

   Без `gh` и без скачивания файла: подпишите его SHA-256 из `.sha256` черновика и вставьте
   подпись в Actions → «Publish signed release» (тег и `ИМЯ=ПОДПИСЬ`). Workflow сам проверит
   подпись открытым ключом из репозитория, приложит `.sig` и опубликует черновик:

   ```sh
   printf '%s' "<sha256 из .sha256>" | xxd -r -p >hash.bin
   openssl pkeyutl -sign -inkey netlane-release.key -in hash.bin -pkeyopt digest:sha256 | base64 -w0
   ```
   Для клиентов файл — `Netlane-windows-x64.zip` и `Netlane-macos.zip`, подпись — `ИМЯ.sig`.
3. После публикации релиз неизменяем (Immutable releases): ассеты нельзя заменить.

## Проверка

```sh
openssl dgst -sha256 -verify netlane-release.pub -signature trusttunnel-dev.tar.gz.sig trusttunnel-dev.tar.gz
```

`Verified OK` — комплект подписан владельцем. Обновлятор, кроме подписи, сверяет версию внутри
комплекта с тегом и не ставит версию старше установленной.

Своя сборка (`NETLANE_REPO=owner/repo`) подписывается своим ключом; путь к его открытой части
задаёт `NETLANE_RELEASE_KEY_FILE` при запуске `netlane-update` от root.
