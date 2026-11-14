# Резервные копии

`trusttunnel-backup` каждую ночь (около 04:00 MSK) кладёт архив в `/var/backups/trusttunnel`
и хранит 14 последних. В архиве база, ключ шифрования паролей VPN, `config.json` и TLS-ключи:
кто получил архив, получил и сервер.

## Шифрование (рекомендуется)

Архив шифруется открытым ключом [age](https://age-encryption.org); секретный ключ остаётся у
владельца, на VPS незашифрованная копия не хранится.

1. На своём компьютере: `age-keygen -o netlane-backup.key` — строка `# public key: age1…` в
   выводе и есть открытый ключ. Файл `netlane-backup.key` храните вне VPS.
2. На сервере от root:

   ```sh
   apt-get install -y age
   install -d -m 0700 /etc/netlane-backup
   echo 'age1…' >/etc/netlane-backup/age-recipients
   systemctl start trusttunnel-backup.service
   ```

3. В панели «Сервер → Обслуживание» появится «Шифрование копий: age».

Расшифровка при восстановлении: `age -d -i netlane-backup.key backup-….tar.gz.age >backup.tar.gz`.

## Копия вне VPS

Копия на том же сервере не спасает при потере VPS. Забирайте зашифрованные архивы к себе,
например `scp root@vpn.example.com:/var/backups/trusttunnel/backup-*.age .` или по расписанию
`rclone copy` в хранилище с правом только на запись.

Время и размер последней копии видны в панели («Сервер → Обслуживание»): копия пишет короткий
отчёт в `/var/lib/netlane-update/report/backup.json`.
