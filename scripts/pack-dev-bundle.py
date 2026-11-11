#!/usr/bin/env python3
"""Assemble the Linux installation bundle (and optionally the Debian package).

Windows has no GNU tar with POSIX modes and no dpkg-deb, so the archives are written
here with explicit modes instead of inheriting them from the file system. The layout
matches scripts/build-dev-bundle.sh: same entries, same order, same permissions and LF
endings in every shell, unit and fail2ban file.

    python scripts/pack-dev-bundle.py --repo . --artifacts artifacts
    python scripts/pack-dev-bundle.py --repo . --artifacts artifacts --deb --version 0.1.0~dev
"""
import argparse
import hashlib
import io
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile

# Entries of the bundle with their modes, in the order GNU tar wrote them.
BUNDLE = [
    ('./', 0o700, True),
    ('./fail2ban/', 0o755, True),
    ('./fail2ban/trusttunnel-admin.filter.conf', 0o755, False),
    ('./fail2ban/trusttunnel-admin.jail.local', 0o755, False),
    ('./install-support.py', 0o755, False),
    ('./SOURCE_COMMIT', 0o644, False),
    ('./install.sh', 0o755, False),
    ('./update.sh', 0o755, False),
    ('./geoip-update', 0o755, False),
    ('./bin/', 0o755, True),
    ('./bin/trusttunnel-admin', 0o755, False),
    ('./bin/trusttunnel_endpoint', 0o755, False),
    ('./setup.sh', 0o755, False),
    ('./backup/', 0o755, True),
    ('./backup/trusttunnel-backup.service', 0o755, False),
    ('./backup/trusttunnel-backup', 0o755, False),
    ('./backup/trusttunnel-backup.timer', 0o755, False),
    ('./geoip/', 0o755, True),
    ('./geoip/trusttunnel-geoip.service', 0o644, False),
    ('./geoip/trusttunnel-geoip.timer', 0o644, False),
]

# Source of every entry, relative to the repository root. Empty means the file is generated.
SOURCES = {
    './fail2ban/trusttunnel-admin.filter.conf': 'deploy/fail2ban/trusttunnel-admin.filter.conf',
    './fail2ban/trusttunnel-admin.jail.local': 'deploy/fail2ban/trusttunnel-admin.jail.local',
    './install-support.py': 'deploy/dev/install-support.py',
    './install.sh': 'deploy/dev/install.sh',
    './setup.sh': 'scripts/netlane-setup.sh',
    './update.sh': 'scripts/netlane-update.sh',
    './backup/trusttunnel-backup.service': 'deploy/backup/trusttunnel-backup.service',
    './backup/trusttunnel-backup': 'deploy/backup/trusttunnel-backup',
    './backup/trusttunnel-backup.timer': 'deploy/backup/trusttunnel-backup.timer',
    './geoip-update': 'scripts/geoip-update.sh',
    './geoip/trusttunnel-geoip.service': 'deploy/systemd/trusttunnel-geoip.service',
    './geoip/trusttunnel-geoip.timer': 'deploy/systemd/trusttunnel-geoip.timer',
}

TEXT_SUFFIXES = ('.sh', '.py', '.conf', '.local', '.service', '.timer')


def run(*args):
    result = subprocess.run(args, capture_output=True, text=True)
    if result.returncode != 0:
        sys.exit(f'{" ".join(args)} failed: {result.stderr.strip()}')
    return result.stdout.strip()


def read(path):
    """Read a file; text files lose CRLF so the VPS gets LF."""
    with open(path, 'rb') as handle:
        data = handle.read()
    return data.replace(b'\r\n', b'\n') if path.endswith(TEXT_SUFFIXES) else data


def add(tar, name, data, mode, mtime):
    info = tarfile.TarInfo(name)
    info.mode, info.uid, info.gid = mode, 0, 0
    info.uname = info.gname = 'root'
    info.mtime = mtime
    if name.endswith('/'):
        info.type = tarfile.DIRTYPE
        tar.addfile(info)
        return
    info.size = len(data)
    tar.addfile(info, io.BytesIO(data))


def write_bundle(repo, artifacts, commit):
    payload = {}
    for name in SOURCES:
        path = os.path.join(repo, SOURCES[name].replace('/', os.sep))
        if not os.path.isfile(path):
            sys.exit(f'missing source for {name}: {path}')
        payload[name] = read(path)
    for name in ('./bin/trusttunnel-admin', './bin/trusttunnel_endpoint'):
        path = os.path.join(artifacts, os.path.basename(name))
        if not os.path.isfile(path):
            sys.exit(f'missing {name}: build it first, or pass --endpoint')
        with open(path, 'rb') as handle:
            payload[name] = handle.read()

    mtime = int(os.path.getmtime(os.path.join(repo, 'deploy', 'dev', 'install.sh')))
    out = os.path.join(artifacts, 'trusttunnel-dev.tar.gz')
    with tarfile.open(out, 'w:gz', format=tarfile.GNU_FORMAT) as tar:
        for name, mode, _ in BUNDLE:
            data = (commit + '\n').encode() if name == './SOURCE_COMMIT' else payload.get(name, b'')
            add(tar, name, data, mode, mtime)

    digest = hashlib.sha256(open(out, 'rb').read()).hexdigest()
    with open(out + '.sha256', 'w', encoding='utf-8', newline='\n') as handle:
        handle.write(f'{digest}  trusttunnel-dev.tar.gz\n')
    return out, digest


def ar_member(archive, name, data, mtime):
    """Append one member in the ar format dpkg-deb produces."""
    header = '{:<16}{:<12}{:<6}{:<6}{:<8o}{:<10}'.format(
        name + '/', mtime, 0, 0, 0o100644, len(data)).encode() + b'`\n'
    archive.write(header)
    archive.write(data)
    if len(data) % 2:
        archive.write(b'\n')


def write_deb(repo, artifacts, version, panel):
    """Build trusttunnel-admin_<version>_amd64.deb: control, postinst, unit and the panel."""
    control = (
        'Package: trusttunnel-admin\n'
        f'Version: {version}\n'
        'Architecture: amd64\n'
        'Maintainer: Netlane Console contributors\n'
        'Depends: adduser, util-linux, ca-certificates\n'
        'Section: admin\n'
        'Priority: optional\n'
        'Description: Netlane Console, private TrustTunnel VPN administration panel\n'
        ' User definitions, policy storage and local metrics adapter.\n'
        ' This preview does not yet apply VPN policies or install TrustTunnel.\n'
    )
    postinst = os.path.join(repo, 'deploy', 'debian', 'postinst')
    unit = os.path.join(repo, 'deploy', 'systemd', 'trusttunnel-admin.service')
    for path in (postinst, unit):
        if not os.path.isfile(path):
            sys.exit(f'missing {path}')

    stage = tempfile.mkdtemp(prefix='netlane-deb-')
    try:
        def pack(name, entries, mtime):
            path = os.path.join(stage, name)
            with tarfile.open(path, 'w:gz', format=tarfile.GNU_FORMAT) as tar:
                for entry, data, mode in entries:
                    add(tar, entry, data, mode, mtime)
            with open(path, 'rb') as handle:
                return handle.read()

        mtime = int(os.path.getmtime(postinst))
        control_tar = pack('control.tar.gz', [
            ('./', b'', 0o755),
            ('./control', control.encode(), 0o644),
            ('./postinst', read(postinst), 0o755),
        ], mtime)
        data_tar = pack('data.tar.gz', [
            ('./', b'', 0o755),
            ('./usr/', b'', 0o755),
            ('./usr/bin/', b'', 0o755),
            ('./usr/bin/trusttunnel-admin', panel, 0o755),
            ('./usr/lib/', b'', 0o755),
            ('./usr/lib/systemd/', b'', 0o755),
            ('./usr/lib/systemd/system/', b'', 0o755),
            ('./usr/lib/systemd/system/trusttunnel-admin.service', read(unit), 0o644),
        ], mtime)

        out = os.path.join(artifacts, f'trusttunnel-admin_{version}_amd64.deb')
        with open(out, 'wb') as archive:
            archive.write(b'!<arch>\n')
            ar_member(archive, 'debian-binary', b'2.0\n', mtime)
            ar_member(archive, 'control.tar.gz', control_tar, mtime)
            ar_member(archive, 'data.tar.gz', data_tar, mtime)
        return out
    finally:
        shutil.rmtree(stage, ignore_errors=True)


def main():
    parser = argparse.ArgumentParser(description='Assemble the Netlane Linux bundle.')
    parser.add_argument('--repo', default='.')
    parser.add_argument('--artifacts', default='artifacts')
    parser.add_argument('--version', help='version recorded in the Debian package')
    parser.add_argument('--deb', action='store_true', help='also build the Debian package')
    args = parser.parse_args()

    repo = os.path.abspath(args.repo)
    artifacts = os.path.abspath(args.artifacts)
    commit = run('git', '-C', repo, 'rev-parse', 'HEAD')

    bundle, digest = write_bundle(repo, artifacts, commit)
    print(f'SOURCE_COMMIT {commit}')
    print(f'bundle        {bundle}  ({os.path.getsize(bundle) / 1048576:.2f} MB)')
    print(f'sha256        {digest}')

    if args.deb:
        with open(os.path.join(artifacts, 'trusttunnel-admin'), 'rb') as handle:
            panel = handle.read()
        deb = write_deb(repo, artifacts, args.version or f'dev-{commit[:7]}', panel)
        print(f'deb           {deb}  ({os.path.getsize(deb) / 1048576:.2f} MB)')


if __name__ == '__main__':
    main()
