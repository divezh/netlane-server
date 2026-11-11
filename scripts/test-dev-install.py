"""Real Ubuntu 24.04/systemd installation and SIGKILL recovery, in disposable Docker containers.

No ports, host filesystem or host cgroup mounts are shared. ACME issuance alone is
replaced by a local trusted test certificate; systemd, binaries, TLS and Fail2ban are real.
Usage: python scripts/test-dev-install.py --panel artifacts/trusttunnel-admin --endpoint artifacts/trusttunnel_endpoint
"""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser(__doc__)
parser.add_argument('--panel', type=Path, required=True)
parser.add_argument('--endpoint', type=Path, required=True)
parser.add_argument('--image', default='netlane-installer-ubuntu24')
parser.add_argument('--skip-build', action='store_true')
parser.add_argument('--ubuntu-version', choices=['24.04', '26.04'], default='24.04')
parser.add_argument('--case', choices=['clean','public','bad-exec','interrupted','allowlist','orphan'])
args = parser.parse_args()
DOMAIN = 'installer.example.test'
PASSWORD = 'disposable-installer-password-1249'
RESULTS = ROOT / 'artifacts' / 'installer-tests'
RESULTS.mkdir(parents=True, exist_ok=True)


def docker(*argv, **kwargs):
    return subprocess.run(['docker', *argv], text=True, encoding="utf-8", errors="replace", capture_output=True, check=True, **kwargs)


BOOTSTRAP = r'''set -euo pipefail
test -f /.dockerenv
test "$(. /etc/os-release; echo "$ID:$VERSION_ID")" = ubuntu:@UBUNTU_VERSION@
mkdir -p /root/netlane /test-bin /run/sshd
chmod 700 /root/netlane
ssh-keygen -A >/dev/null
systemctl start ssh
echo '127.0.0.1 installer.example.test' >>/etc/hosts
# Only the external ACME issuance is simulated. The resulting certificate is trusted
# by the container, so HTTPS checks still validate name, trust and expiry.
cat >/test-bin/certbot <<'MOCK'
#!/bin/bash
set -eu
domain=installer.example.test
mkdir -p /etc/letsencrypt/live/$domain
openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj /CN=$domain \
 -addext subjectAltName=DNS:$domain -keyout /etc/letsencrypt/live/$domain/privkey.pem \
 -out /etc/letsencrypt/live/$domain/fullchain.pem >/dev/null 2>&1
cp /etc/letsencrypt/live/$domain/fullchain.pem /usr/local/share/ca-certificates/netlane-test.crt
update-ca-certificates >/dev/null 2>&1
MOCK
cat >/test-bin/curl <<'MOCK'
#!/bin/bash
case "${*: -1}" in
 https://api.ipify.org|https://ifconfig.me/ip|https://icanhazip.com) echo 127.0.0.1;exit 0;;
 https://ipinfo.io/*/country) echo NL;exit 0;;
esac
exec /usr/bin/curl "$@"
MOCK
cat >/test-bin/systemctl <<'MOCK'
#!/bin/bash
if [[ ${NETLANE_TEST_PAUSE:-0} == 1 && $1 == daemon-reload ]]; then
 touch /run/netlane-test-ready
 while true; do sleep 1; done
fi
exec /usr/bin/systemctl "$@"
MOCK
cat >/test-bin/runuser <<'MOCK'
#!/bin/bash
if [[ ${NETLANE_TEST_BAD_EXEC:-0} == 1 && ${*: -1} == --version ]]; then exit 126; fi
exec /usr/sbin/runuser "$@"
MOCK
chmod 755 /test-bin/*
# Reproduce the reported root-only parents before every fresh installation.
install -d -m 700 -o root -g root /opt/trusttunnel /opt/trusttunnel-admin
'''

VERIFY = r'''
import json,os,pwd,stat,subprocess,urllib.request,ssl,sqlite3
from pathlib import Path
def run(*args): subprocess.run(args,check=True,stdout=subprocess.DEVNULL)
for base,group in [('/opt/trusttunnel','trusttunnel'),('/opt/trusttunnel-admin','trusttunnel-admin')]:
 for path in [base,base+'/bin']:
  st=os.stat(path);assert st.st_uid==0 and st.st_gid==pwd.getpwnam(group).pw_gid and stat.S_IMODE(st.st_mode)==0o750,(path,oct(st.st_mode))
 binary=base+'/bin/'+('trusttunnel_endpoint' if group=='trusttunnel' else 'trusttunnel-admin')
 st=os.stat(binary);assert st.st_uid==0 and stat.S_IMODE(st.st_mode)==0o750
 run('runuser','-u',group,'--',binary,'--version' if group=='trusttunnel' else 'version')
for account in ['trusttunnel','trusttunnel-admin']:
 for name in ['fullchain.pem','privkey.pem']:
  path=f'/etc/{account}/tls/{name}';st=os.stat(path)
  assert st.st_uid==pwd.getpwnam(account).pw_uid and stat.S_IMODE(st.st_mode)==0o600
  run('runuser','-u',account,'--','test','-r',path)
cfg=json.load(open('/etc/trusttunnel-admin/config.json'))
expected=os.environ.get('EXPECTED_PANEL_LISTEN','127.0.0.1:8443')
assert cfg['listen']==expected,cfg['listen']
assert stat.S_IMODE(os.stat('/etc/trusttunnel-admin/config.json').st_mode)==0o600
for unit in ['trusttunnel-managed','trusttunnel-admin','fail2ban','certbot.timer','trusttunnel-backup.timer']:
 run('systemctl','is-active','--quiet',unit)
for unit in ['trusttunnel-managed','trusttunnel-admin']:
 account='trusttunnel' if unit=='trusttunnel-managed' else 'trusttunnel-admin'
 assert subprocess.check_output(['systemctl','show',unit,'-p','User','--value'],text=True).strip()==account
 pid=subprocess.check_output(['systemctl','show',unit,'-p','MainPID','--value'],text=True).strip()
 uidline=next(line for line in Path('/proc/'+pid+'/status').read_text().splitlines() if line.startswith('Uid:'))
 assert int(uidline.split()[1])==pwd.getpwnam(account).pw_uid!=0
 for prop,expected_property in [('NoNewPrivileges','yes'),('PrivateTmp','yes'),('ProtectHome','yes'),('ProtectSystem','strict')]:
  value=subprocess.check_output(['systemctl','show',unit,'-p',prop,'--value'],text=True).strip();assert value==expected_property,(unit,prop,value)
run('runuser','-u','trusttunnel-admin','--','/opt/trusttunnel-admin/bin/trusttunnel-admin','doctor')
run('fail2ban-client','status','trusttunnel-admin')
run('systemd-analyze','verify','/etc/systemd/system/trusttunnel-managed.service','/etc/systemd/system/trusttunnel-admin.service','/etc/systemd/system/trusttunnel-backup.service','/etc/systemd/system/trusttunnel-backup.timer')
run('sh','-n','/etc/letsencrypt/renewal-hooks/deploy/trusttunnel-dev')
for port,path in [(8443,'/healthz'),(443,'/')]:
 run('curl','--fail','--silent','--resolve',f'installer.example.test:{port}:127.0.0.1',f'https://installer.example.test:{port}{path}')
# No unsafe public panel listener; verify the actual kernel socket listing as well.
listeners=subprocess.check_output(['ss','-ltn'],text=True)
if expected.startswith('127.'):
 assert '0.0.0.0:8443' not in listeners and '[::]:8443' not in listeners
 assert '8443' not in subprocess.check_output(['ufw','status'],text=True)
elif os.environ.get('EXPECTED_PANEL_PUBLIC')=='1':
 rules=subprocess.check_output(['ufw','status'],text=True)
 assert any('8443' in line and 'Anywhere' in line for line in rules.splitlines()),rules
else:
 rules=subprocess.check_output(['ufw','status'],text=True)
 assert '198.51.100.7' in rules and not any('8443' in line and 'Anywhere' in line for line in rules.splitlines())
run('runuser','-u','trusttunnel-admin','--','python3','-c','import os; assert os.access("/run/trusttunnel/control.sock", os.R_OK | os.W_OK)')
assert json.load(open('/var/lib/netlane-install/state.json'))['complete'] is True
print('Verified service users, permissions, hardening, timers, Fail2ban, trusted HTTPS and control socket.')
'''


def execute(name, command, *, fail=False, timeout=240):
    result = subprocess.run(['docker', 'exec', '-e', 'PATH=/test-bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin', '-e', 'NO_PROXY=*', name, 'bash', '-c', command], text=True, encoding="utf-8", errors="replace", capture_output=True, timeout=timeout)
    if (result.returncode == 0) == fail:
        raise AssertionError(result.stdout + result.stderr)
    return result


def scenario(bundle, kind):
    name = 'netlane-install-' + kind + '-' + uuid.uuid4().hex[:8]
    log = []
    try:
        docker('run', '-d', '--name', name, '--privileged', '--cgroupns=private', '--tmpfs', '/run', '--tmpfs', '/run/lock', '--tmpfs', '/tmp', args.image)
        for _ in range(100):
            if subprocess.run(['docker', 'exec', name, 'test', '-d', '/run/systemd/system'], capture_output=True).returncode == 0:
                break
            time.sleep(.1)
        log.append(execute(name, BOOTSTRAP.replace('@UBUNTU_VERSION@', args.ubuntu_version)).stdout)
        docker('cp', str(bundle) + '/.', name + ':/root/netlane')
        execute(name, 'chmod 755 /root/netlane/{setup,install}.sh /root/netlane/bin/* /root/netlane/backup/trusttunnel-backup')
        install = f"printf '%s\\n' '{PASSWORD}' | TT_PASSWORD_STDIN=1 bash /root/netlane/install.sh {DOMAIN}"
        if kind=='orphan':
            execute(name,"mkdir -p /var/lib/trusttunnel-admin; printf sentinel >/var/lib/trusttunnel-admin/panel.sqlite")
            execute(name,'export TT_RESUME=1; '+install,fail=True)
            execute(name,'test "$(cat /var/lib/trusttunnel-admin/panel.sqlite)" = sentinel; test ! -e /etc/trusttunnel-admin/config.json')
            print(f'PASS Ubuntu {args.ubuntu_version}: orphan database preserved',flush=True)
            return
        if kind in ('clean','public','bad-exec','allowlist'):
            # Bad binary execution must fail before creating credentials/config/database.
            if kind == 'bad-exec':
                execute(name, 'export NETLANE_TEST_BAD_EXEC=1; ' + install, fail=True)
                execute(name, 'test ! -e /etc/trusttunnel-admin/config.json; test ! -e /var/lib/trusttunnel-admin/panel.sqlite; test ! -e /etc/trusttunnel/vpn.toml')
            flag=' --resume' if kind=='bad-exec' else ''
            access='NETLANE_PANEL_ALLOW=198.51.100.7 ' if kind=='allowlist' else 'NETLANE_PANEL_PUBLIC=1 ' if kind=='public' else ''
            setup = access+f'NETLANE_YES=1 NETLANE_DOMAIN={DOMAIN} NETLANE_UPGRADE=0 NETLANE_AUTO_UPDATES=0 NETLANE_SWAP=0 NETLANE_SERVER_COUNTRY=NL bash /root/netlane/setup.sh'+flag
            log.append(execute(name, setup).stdout)
        else:
            # Pause using a test-only command wrapper, then SIGKILL the actual installer
            # process group after credentials, database, units and hooks were written.
            pause = execute(name, "setsid bash -c \"export NETLANE_TEST_PAUSE=1; " + install + "\" >/tmp/interrupted.log 2>&1 & echo $!")
            pid = int(pause.stdout.strip())
            for _ in range(600):
                if subprocess.run(['docker', 'exec', name, 'test', '-f', '/run/netlane-test-ready'], capture_output=True).returncode == 0:
                    break
                time.sleep(.1)
            else:
                raise AssertionError(execute(name, 'cat /tmp/interrupted.log').stdout)
            execute(name, f'kill -KILL -- -{pid}')
            before = execute(name, "python3 - <<'PY'\nimport json,sqlite3\nfrom pathlib import Path\nc=json.load(open('/etc/trusttunnel-admin/config.json'));print(json.dumps(c,sort_keys=True))\ndb=sqlite3.connect(c['database']);db.execute('CREATE TABLE resume_sentinel(value TEXT)');db.execute(\"INSERT INTO resume_sentinel VALUES('keep-me')\");db.commit()\nPY").stdout.strip()
            # The same command without explicit resume must refuse safely.
            execute(name, install, fail=True)
            execute(name, 'chmod 700 /opt/trusttunnel /opt/trusttunnel-admin')
            log.append(execute(name, 'export TT_RESUME=1; ' + install).stdout)
            after = execute(name, "python3 - <<'PY'\nimport json,sqlite3\nc=json.load(open('/etc/trusttunnel-admin/config.json'));c.pop('sync_prefix',None);print(json.dumps(c,sort_keys=True))\nassert sqlite3.connect(c['database']).execute('SELECT value FROM resume_sentinel').fetchone()[0]=='keep-me'\nPY").stdout.strip()
            old = json.loads(before); old.pop('sync_prefix', None)
            assert json.loads(after) == old, 'Resume changed existing config/secrets'
            # An interrupted root-only parent is repaired on resume too.
        expected="EXPECTED_PANEL_LISTEN=0.0.0.0:8443 " if kind in ('allowlist','public') else ""
        if kind=='public': expected+='EXPECTED_PANEL_PUBLIC=1 '
        log.append(execute(name, expected+"python3 - <<'PY'\n" + VERIFY + '\nPY').stdout)
        if kind=='clean':
            # Exercise the generated renewal hook (not just its syntax), without
            # restarting the VPN endpoint. The admin may restart to load TLS.
            execute(name, 'vpn_pid=$(systemctl show trusttunnel-managed -p MainPID --value); RENEWED_LINEAGE=/etc/letsencrypt/live/installer.example.test sh /etc/letsencrypt/renewal-hooks/deploy/trusttunnel-dev; test "$vpn_pid" = "$(systemctl show trusttunnel-managed -p MainPID --value)"')
            # Backups run as root, must traverse private parents and restore both units.
            execute(name,"""systemctl start trusttunnel-backup.service; test -n "$(find /var/backups/trusttunnel -name 'backup-*.tar.gz' -print -quit)" """)
            for attempt in range(100):
                result=subprocess.run(['docker','exec',name,'runuser','-u','trusttunnel-admin','--','/opt/trusttunnel-admin/bin/trusttunnel-admin','doctor'],capture_output=True)
                if result.returncode==0:break
                time.sleep(.1)
            else:raise AssertionError('Services not restored after test backup')
            print(f'PASS Ubuntu {args.ubuntu_version}: real renewal hook and backup unit',flush=True)
            # Run the actual file updater and preserve a locally customized schedule.
            execute(name, "sed -i 's/^Description=.*/Description=Daily Netlane backup at legacy-zone/; s/^OnCalendar=.*/OnCalendar=*-*-* 01:17:00 UTC/' /etc/systemd/system/trusttunnel-backup.timer; systemctl daemon-reload; printf '%040d\\n' 1 >/root/netlane/SOURCE_COMMIT; tar -czf /root/update.tar.gz -C /root/netlane .; sha256sum /root/update.tar.gz >/root/update.tar.gz.sha256")
            log.append(execute(name, 'bash /root/netlane/update.sh --file /root/update.tar.gz').stdout)
            execute(name, "grep -F 'Description=Daily Netlane backup at 04:00 MSK' /etc/systemd/system/trusttunnel-backup.timer; grep -Fx 'OnCalendar=*-*-* 01:17:00 UTC' /etc/systemd/system/trusttunnel-backup.timer")
            print(f'PASS Ubuntu {args.ubuntu_version}: actual updater, MSK label and preserved custom schedule',flush=True)
        # A completed installation is not an updater. It must refuse both entry points.
        execute(name, 'export TT_RESUME=1; ' + install, fail=True)
        execute(name, f'NETLANE_YES=1 NETLANE_DOMAIN={DOMAIN} bash /root/netlane/setup.sh --resume', fail=True)
        print(f'PASS Ubuntu {args.ubuntu_version}:', kind, flush=True)
    finally:
        access = subprocess.run(['docker', 'exec', name, 'bash', '-c', "stat -c '%a %U:%G %n' /run/trusttunnel /run/trusttunnel/control.sock 2>&1; runuser -u trusttunnel-admin -- id; runuser -u trusttunnel-admin -- /usr/bin/test -r /run/trusttunnel/control.sock; echo read_status=$?; runuser -u trusttunnel-admin -- /usr/bin/test -w /run/trusttunnel/control.sock; echo write_status=$?"], text=True, capture_output=True)
        diagnostics = subprocess.run(['docker', 'exec', name, 'journalctl', '-b', '--no-pager', '-u', 'trusttunnel-managed', '-u', 'trusttunnel-admin', '-u', 'fail2ban'], text=True, encoding="utf-8", errors="replace", capture_output=True)
        (RESULTS / (kind + '.log')).write_text('\n'.join(log) + '\n' + access.stdout + access.stderr + diagnostics.stdout + diagnostics.stderr, encoding='utf-8')
        subprocess.run(['docker', 'rm', '-f', name], capture_output=True)


if not args.skip_build:
    subprocess.run(['docker', 'build', '--build-arg', 'UBUNTU_VERSION=' + args.ubuntu_version, '-t', args.image, '-f', str(ROOT / 'deploy/ci/installer.Dockerfile'), str(ROOT / 'deploy/ci')], check=True)
with tempfile.TemporaryDirectory(prefix='netlane-install-test-') as directory:
    bundle = Path(directory)
    for sub in ['bin', 'backup', 'fail2ban']:
        (bundle / sub).mkdir()
    mapping = {'install.sh': ROOT / 'deploy/dev/install.sh', 'install-support.py': ROOT / 'deploy/dev/install-support.py', 'setup.sh': ROOT / 'scripts/netlane-setup.sh', 'update.sh': ROOT / 'scripts/netlane-update.sh'}
    for folder in ['backup', 'fail2ban']:
        for file in (ROOT / 'deploy' / folder).iterdir():
            if file.is_file(): mapping[folder + '/' + file.name] = file
    for dest, source in mapping.items():
        (bundle / dest).write_bytes(source.read_bytes().replace(b'\r\n', b'\n'))
    shutil.copyfile(args.panel.resolve(), bundle / 'bin/trusttunnel-admin')
    shutil.copyfile(args.endpoint.resolve(), bundle / 'bin/trusttunnel_endpoint')
    for kind in ([args.case] if args.case else ['clean', 'public', 'bad-exec', 'interrupted', 'allowlist', 'orphan']):
        scenario(bundle, kind)
