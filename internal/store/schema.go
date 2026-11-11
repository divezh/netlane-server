package store

import "database/sql"

// migrate adds the node, sync and subscription tables. They are additive, so the schema
// version stays 1 and the previous panel can still open the database after a rollback.
func migrate(db *sql.DB) error {
	_, err := db.Exec(`BEGIN;
 CREATE TABLE IF NOT EXISTS nodes(id TEXT PRIMARY KEY, name TEXT NOT NULL, country TEXT NOT NULL, domain TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL DEFAULT '', applied_revision INTEGER NOT NULL DEFAULT 0, endpoint_version TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS node_invites(code_hash TEXT PRIMARY KEY, expires_at TEXT NOT NULL, used_at TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS node_user_state(node_id TEXT NOT NULL, user_id TEXT NOT NULL, revision INTEGER NOT NULL, PRIMARY KEY(node_id,user_id));
 CREATE TABLE IF NOT EXISTS node_commands(id INTEGER PRIMARY KEY, node_id TEXT NOT NULL, kind TEXT NOT NULL, username TEXT NOT NULL, created_at TEXT NOT NULL, acked_at TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS node_usage(node_id TEXT NOT NULL, user_id TEXT NOT NULL, month TEXT NOT NULL, upload_bytes INTEGER NOT NULL, download_bytes INTEGER NOT NULL, PRIMARY KEY(node_id,user_id,month));
 CREATE TABLE IF NOT EXISTS subscriptions(user_id TEXT PRIMARY KEY, token_hash TEXT NOT NULL UNIQUE, token_sealed TEXT NOT NULL, created_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS distributed_users(user_id TEXT PRIMARY KEY, username TEXT NOT NULL, credential TEXT NOT NULL, enabled INTEGER NOT NULL, download_mbps INTEGER NOT NULL, upload_mbps INTEGER NOT NULL, expires_at TEXT NOT NULL, revision INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS client_settings(id INTEGER PRIMARY KEY CHECK(id=1),revision INTEGER NOT NULL,settings TEXT NOT NULL);
 INSERT OR IGNORE INTO client_settings(id,revision,settings) VALUES(1,0,'{"version":1,"revision":0,"dns_servers":null,"rule_sets":[]}');
 CREATE TABLE IF NOT EXISTS admin_factor(id INTEGER PRIMARY KEY CHECK(id=1), sealed TEXT NOT NULL);
 INSERT OR IGNORE INTO admin_factor(id,sealed) VALUES(1,'');
 INSERT OR IGNORE INTO meta(key,value) VALUES('users_revision',0);
 CREATE TABLE IF NOT EXISTS statistics_samples(at INTEGER PRIMARY KEY,data TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS statistics_baseline(id INTEGER PRIMARY KEY CHECK(id=1),at INTEGER NOT NULL,counters TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS location_statistics_samples(source TEXT NOT NULL,at INTEGER NOT NULL,data TEXT NOT NULL,PRIMARY KEY(source,at));
 CREATE TABLE IF NOT EXISTS location_statistics_baseline(source TEXT PRIMARY KEY,at INTEGER NOT NULL,counters TEXT NOT NULL);
 INSERT OR IGNORE INTO location_statistics_samples SELECT 'main',at,data FROM statistics_samples WHERE NOT EXISTS(SELECT 1 FROM meta WHERE key='location_statistics_migrated');
 INSERT OR IGNORE INTO location_statistics_baseline SELECT 'main',at,counters FROM statistics_baseline WHERE NOT EXISTS(SELECT 1 FROM meta WHERE key='location_statistics_migrated');
 INSERT OR IGNORE INTO meta(key,value) VALUES('location_statistics_migrated',1);
 CREATE TABLE IF NOT EXISTS subscription_visits(day TEXT NOT NULL,token_hash TEXT NOT NULL,client TEXT NOT NULL,PRIMARY KEY(day,token_hash));
 CREATE INDEX IF NOT EXISTS subscription_visits_token ON subscription_visits(token_hash,day);
 CREATE TABLE IF NOT EXISTS groups(id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS group_locations(group_id TEXT NOT NULL, location_id TEXT NOT NULL, PRIMARY KEY(group_id,location_id));
 CREATE TABLE IF NOT EXISTS user_groups(user_id TEXT PRIMARY KEY, group_id TEXT NOT NULL);
 INSERT OR IGNORE INTO groups(id,name,created_at) VALUES('all','Все локации',datetime('now'));
 INSERT OR IGNORE INTO user_groups(user_id,group_id) SELECT id,'all' FROM users WHERE id NOT IN(SELECT user_id FROM user_groups);
 DELETE FROM user_groups WHERE user_id NOT IN(SELECT id FROM users);
 COMMIT;`)
	return err
}
