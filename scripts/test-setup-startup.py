"""Local Linux regressions for early setup output and pipefail-safe SSH detection."""
from pathlib import Path
import subprocess
import os
import tempfile

source = Path(__file__).with_name('netlane-setup.sh').read_text()
prefix = source.split('ask() {', 1)[0]
result = subprocess.run(['bash', '-c', prefix + '\nprivate_value=must-not-appear\nfalse\n'], text=True, capture_output=True)
assert result.returncode != 0 and 'Ошибка установщика: строка' in result.stderr, result
assert 'must-not-appear' not in result.stderr + result.stdout

detect = next(line for line in source.splitlines() if line.startswith('ssh_port=$(sshd'))
# Delay after the first line forces a closed pipe if the consumer exits early.
producer = 'sshd() { printf "port 2222\\n"; sleep .05; printf "%100000s\\n" x; }\n'
result = subprocess.run(['bash', '-c', 'set -euo pipefail\n' + producer + detect + '\n[[ $ssh_port == 2222 ]]'], text=True, capture_output=True)
assert result.returncode == 0, result
install = Path(__file__).resolve().parent.parent / 'deploy/dev/install.sh'
detect = next(line.strip() for line in install.read_text().splitlines() if line.strip().startswith('ssh_port=$(sshd'))
result = subprocess.run(['bash', '-c', 'set -euo pipefail\n' + producer + detect + '\n[[ $ssh_port == 2222 ]]'], text=True, capture_output=True)
assert result.returncode == 0, result
permission_check = 'require_access() {' + install.read_text().split('require_access() {', 1)[1].split('\n}\n', 1)[0] + '\n}\n'
with tempfile.TemporaryDirectory() as directory:
    os.chmod(directory, 0o755)
    public = Path(directory) / 'public'
    private = Path(directory) / 'private'
    public.write_text('readable'); private.write_text('private')
    public.chmod(0o644); private.chmod(0o600)
    result = subprocess.run(['bash', '-c', permission_check + 'require_access nobody r "$1"', '_', str(public)], text=True, capture_output=True)
    assert result.returncode == 0, result
    result = subprocess.run(['bash', '-c', permission_check + 'require_access nobody r "$1"', '_', str(private)], text=True, capture_output=True)
    assert result.returncode != 0 and 'denied' in result.stderr, result
print('PASS: early errors report line/status without secrets; SSH probe drains full output under pipefail')
