#!/usr/bin/env python3
"""Atomic root-only installation state; never adopt a running unmarked installation."""
import json
import os
from pathlib import Path
import pwd
import secrets
import subprocess
import stat
import sys
import tempfile
import tomllib

STATE_DIR = Path('/var/lib/netlane-install')
LEGACY_STATE_DIR = Path('/var/lib/wtt-install')
# An interrupted installation of the former product name keeps its state where it was.
if not STATE_DIR.exists() and LEGACY_STATE_DIR.exists():
    STATE_DIR = LEGACY_STATE_DIR
STATE = STATE_DIR / 'state.json'
CONFIG = Path('/etc/trusttunnel-admin/config.json')
ARTIFACTS = [Path(p) for p in ('/etc/trusttunnel/vpn.toml', str(CONFIG), '/var/lib/trusttunnel/managed.json', '/var/lib/trusttunnel-admin/panel.sqlite')]


def safe(path):
    path = Path(path)
    for part in [*reversed(path.parents), path]:
        if part.is_symlink():
            raise SystemExit(f'Refusing symlink: {part}')
    return path


def atomic(path, content, mode=0o600, account='root', new=False):
    path = safe(path)
    if new and path.exists():
        return
    user = pwd.getpwnam(account)
    fd, temporary = tempfile.mkstemp(prefix='.netlane-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as f:
            os.fchmod(f.fileno(), mode)
            os.fchown(f.fileno(), user.pw_uid, user.pw_gid)
            f.write(content.encode() if isinstance(content, str) else content)
            f.flush()
            os.fsync(f.fileno())
        if new:
            try:
                os.link(temporary, path)
            except FileExistsError:
                pass
        else:
            os.replace(temporary, path)
        directory = os.open(path.parent, os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def save(state):
    atomic(STATE, json.dumps(state) + '\n')


def load():
    for path in (safe(STATE_DIR), safe(STATE)):
        meta = path.stat()
        if meta.st_uid != 0 or stat.S_IMODE(meta.st_mode) & 0o077:
            raise SystemExit('Untrusted installer state permissions; review manually.')
    state=json.loads(STATE.read_text())
    if state.get('version')!=1: raise SystemExit('Unknown installer state version; review manually.')
    return state


def cfg():
    return json.loads(safe(CONFIG).read_text())


def file_mode(path, account, mode=0o600):
    path = safe(path)
    if not path.exists():
        return
    if not path.is_file():
        raise SystemExit(f'Not a regular file: {path}')
    user = pwd.getpwnam(account)
    os.chown(path, user.pw_uid, user.pw_gid)
    os.chmod(path, mode)


def begin(domain, role, listen, check_only=False):
    for path in ['/opt/trusttunnel/bin', '/opt/trusttunnel-admin/bin', '/etc/trusttunnel/tls', '/etc/trusttunnel-admin/tls', '/var/lib/trusttunnel', '/var/lib/trusttunnel-admin', '/run/trusttunnel', str(STATE)]:
        safe(path)
    if STATE.exists():
        state = load()
        if state.get('complete'):
            raise SystemExit('Installation already completed. Use netlane-update, not setup/install.')
        if os.environ.get('TT_RESUME') != '1':
            raise SystemExit('Interrupted installation. Re-run setup.sh --resume (or TT_RESUME=1 install.sh).')
        if state['domain'] != domain or state['role'] != role:
            raise SystemExit('Resume domain/role differs; existing state preserved.')
    else:
        if any(p.exists() for p in ARTIFACTS) and os.environ.get('TT_RESUME') != '1':
            raise SystemExit('Existing installation requires explicit --resume.')
        for service in ('trusttunnel-managed', 'trusttunnel-admin', 'trusttunnel-node'):
            if subprocess.run(['systemctl', 'is-active', '--quiet', service]).returncode == 0:
                raise SystemExit('Unmarked running installation: refused; use backed-up maintenance.')
        state = dict(version=1, domain=domain, role=role, listen=listen, panel_allow=os.environ.get('TT_PANEL_ALLOW',''), panel_public=os.environ.get('TT_PANEL_PUBLIC','0'), had_config=CONFIG.exists(), config_ready=False, complete=False)
        if role=='node': state['main_url']=os.environ.get('TT_JOIN','').split('/node/join')[0]
    if role=='node' and os.environ.get('TT_JOIN') and state.get('main_url') and os.environ['TT_JOIN'].split('/node/join')[0]!=state['main_url']:
        raise SystemExit('Node main server differs from the interrupted installation.')
    if CONFIG.exists():
        c = cfg()
        if c.get('domain') != domain or (c.get('role') or 'main') != role:
            raise SystemExit('Existing config domain/role differs; preserved.')
        if state['had_config'] and role == 'main':
            expected = c.get('listen', '')
            if not expected.startswith(('127.0.0.1:', '[::1]:')) and not (os.environ.get('TT_PANEL_PUBLIC') == '1' or os.environ.get('TT_PANEL_ALLOW')):
                raise SystemExit('Existing public panel requires explicit TT_PANEL_PUBLIC=1 or TT_PANEL_ALLOW; config preserved.')
            state['listen'] = expected
    elif Path('/var/lib/trusttunnel-admin/panel.sqlite').exists():
        raise SystemExit('Database without config.json: restore original config/encryption key from backup. No data removed.')
    hosts = safe('/etc/trusttunnel/hosts.toml')
    if hosts.exists():
        stored = tomllib.loads(hosts.read_text())
        if not any(h.get('hostname') == domain for h in stored.get('main_hosts', [])):
            raise SystemExit('Existing TLS host differs; preserved.')
    if role == 'main' and not state['listen'].startswith(('127.0.0.1:', '[::1]:')) and not (os.environ.get('TT_PANEL_PUBLIC') == '1' or os.environ.get('TT_PANEL_ALLOW')):
        raise SystemExit('Public panel requires explicit access policy on resume.')
    if check_only:
        return
    safe(STATE_DIR).mkdir(mode=0o700, exist_ok=True)
    os.chown(STATE_DIR, 0, 0)
    os.chmod(STATE_DIR, 0o700)
    save(state)
    print(state['listen'])


def seed():
    state = load()
    domain = state['domain']
    ipv6 = bool(subprocess.check_output(['ip', '-6', 'route', 'show', 'default']).strip()) and bool(subprocess.check_output(['ip', '-6', 'addr', 'show', 'scope', 'global']).strip())
    atomic('/etc/trusttunnel/credentials.toml', '[[client]]\nusername="bootstrap-inactive"\npassword=' + json.dumps(secrets.token_urlsafe(48)) + '\n', account='trusttunnel', new=True)
    atomic('/var/lib/trusttunnel/managed.json', json.dumps(dict(version=1, generation=0, users=[])), account='trusttunnel', new=True)
    atomic('/etc/trusttunnel/vpn.toml', f'''listen_address = "0.0.0.0:443"
auth_failure_status_code = 405
ipv6_available = {str(ipv6).lower()}
allow_private_network_connections = false
credentials_file = "/etc/trusttunnel/credentials.toml"
default_max_http2_conns_per_client = 8
default_max_http3_conns_per_client = 8
[listen_protocols.http1]
[listen_protocols.http2]
[listen_protocols.quic]
[metrics]
address = "127.0.0.1:1987"
per_client_metrics = true
[reverse_proxy]
server_address = "127.0.0.1:8090"
path_mask = "/"
''', account='trusttunnel', new=True)
    atomic('/etc/trusttunnel/hosts.toml', f'[[main_hosts]]\nhostname = {json.dumps(domain)}\ncert_chain_path = "/etc/trusttunnel/tls/fullchain.pem"\nprivate_key_path = "/etc/trusttunnel/tls/privkey.pem"\n', account='trusttunnel', new=True)
    for path in ['/etc/trusttunnel/vpn.toml', '/etc/trusttunnel/hosts.toml', '/etc/trusttunnel/credentials.toml']:
        tomllib.loads(Path(path).read_text())
        file_mode(path, 'trusttunnel')
    json.loads(Path('/var/lib/trusttunnel/managed.json').read_text())
    for path in Path('/var/lib/trusttunnel').iterdir():
        if path.name == 'managed.json' or path.name.startswith('managed.usage.'):
            file_mode(path, 'trusttunnel')


def configure():
    state = load()
    c = cfg()
    if not state['config_ready'] and not state['had_config'] and state['role'] == 'main':
        c.update(listen=state['listen'], control_socket='/run/trusttunnel/control.sock', tls_cert='/etc/trusttunnel-admin/tls/fullchain.pem', tls_key='/etc/trusttunnel-admin/tls/privkey.pem')
        c.setdefault('decoy_listen', '127.0.0.1:8090')
        c.setdefault('sync_prefix', ''.join(secrets.choice('abcdefghijklmnopqrstuvwxyz234567') for _ in range(26)))
        name = os.environ.get('TT_SERVER_NAME', '').strip()
        country = os.environ.get('TT_SERVER_COUNTRY', '').strip().upper()
        if len(name) > 64 or any(ord(ch) < 32 or 127 <= ord(ch) < 160 for ch in name):
            raise SystemExit('Invalid server name')
        if country and (len(country) != 2 or not country.isascii() or not country.isalpha()):
            raise SystemExit('Invalid server country')
        if name:
            c['server_name'] = name
        if country:
            c['server_country'] = country
        atomic(CONFIG, json.dumps(c) + '\n', account='trusttunnel-admin')
    if c.get('domain') != state['domain'] or (c.get('role') or 'main') != state['role']:
        raise SystemExit('Config identity mismatch')
    if c.get('control_socket') != '/run/trusttunnel/control.sock':
        raise SystemExit('Unexpected control socket in preserved config')
    if state['role'] == 'main' and (c.get('listen') != state['listen'] or c.get('tls_cert') != '/etc/trusttunnel-admin/tls/fullchain.pem' or c.get('tls_key') != '/etc/trusttunnel-admin/tls/privkey.pem'):
        raise SystemExit('Preserved config has a different listen/TLS layout; review it manually.')
    file_mode(CONFIG, 'trusttunnel-admin')
    for name in ['panel.sqlite', 'panel.sqlite-wal', 'panel.sqlite-shm', 'node.token', 'node.state']:
        file_mode(Path('/var/lib/trusttunnel-admin') / name, 'trusttunnel-admin')
    state['config_ready'] = True
    save(state)


if __name__ == '__main__':
    command = sys.argv[1]
    if command == 'preflight':
        begin(*sys.argv[2:], check_only=True)
    elif command == 'begin':
        begin(*sys.argv[2:])
    elif command == 'repair-data':
        for name in ['panel.sqlite','panel.sqlite-wal','panel.sqlite-shm','node.token','node.state']:
            file_mode(Path('/var/lib/trusttunnel-admin')/name,'trusttunnel-admin')
    elif command == 'seed':
        seed()
    elif command == 'configure':
        configure()
    elif command == 'file':
        atomic(sys.argv[2], sys.stdin.buffer.read(), int(sys.argv[3], 8), account=sys.argv[4] if len(sys.argv)>4 else 'root')
    elif command == 'complete':
        state = load()
        state['complete'] = True
        save(state)
    else:
        raise SystemExit('Unknown installer operation')
