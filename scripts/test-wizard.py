"""Linux PTY smoke test for the actual setup/serve binary; uses disposable credentials."""
import errno
import json
import os
import pty
import select
import signal
import subprocess
import sys
import tempfile
import termios
import time
import urllib.request
from pathlib import Path


binary = str(Path(sys.argv[1]).resolve())
with tempfile.TemporaryDirectory() as directory:
    config = Path(directory) / "config.json"
    args = [binary, "setup", "--config", str(config), "--data-dir", directory]
    pid, terminal = pty.fork()
    if pid == 0:
        os.execv(binary, args)
    transcript = b""
    child_reaped = False
    password = b"disposable-test-password-8392"
    prompts = [("Домен VPN: ".encode(), b"vpn.example.net"),
               ("символов): ".encode(), password),
               ("Повторите пароль: ".encode(), password)]
    deadline = time.monotonic() + 15
    try:
        while True:
            if time.monotonic() > deadline:
                raise AssertionError("setup wizard timed out")
            ready, _, _ = select.select([terminal], [], [], 0.1)
            if ready:
                try:
                    chunk = os.read(terminal, 8192)
                except OSError as exc:
                    if exc.errno == errno.EIO:
                        break
                    raise
                if not chunk:
                    break
                transcript += chunk
            if prompts and prompts[0][0] in transcript:
                _, answer = prompts[0]
                # ReadPassword must disable terminal echo before we send either password.
                if answer == password and termios.tcgetattr(terminal)[3] & termios.ECHO:
                    continue
                os.write(terminal, answer + b"\n")
                prompts.pop(0)
        _, status = os.waitpid(pid, 0)
        child_reaped = True
        assert os.waitstatus_to_exitcode(status) == 0, "setup failed"
        assert not prompts, "wizard did not ask all three prompts"
        assert password not in transcript, "password was echoed"
        assert config.stat().st_mode & 0o777 == 0o600, "config permissions are not private"
        original = config.read_bytes()
        assert subprocess.run(args, capture_output=True).returncode != 0, "setup overwrote config"
        assert config.read_bytes() == original, "repeat setup changed secrets"
    finally:
        os.close(terminal)
        if not child_reaped:
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            os.waitpid(pid, 0)

    process = subprocess.Popen([binary, "serve", "--config", str(config)], stdout=subprocess.DEVNULL)
    try:
        deadline = time.monotonic() + 10
        while True:
            try:
                with urllib.request.urlopen("http://127.0.0.1:8080/healthz", timeout=1) as response:
                    assert json.load(response)["status"] == "ok"
                break
            except OSError:
                if time.monotonic() > deadline or process.poll() is not None:
                    raise AssertionError("configured server did not start")
                time.sleep(0.1)
        request = urllib.request.Request("http://127.0.0.1:8080/api/v1/auth/login",
            data=json.dumps({"username": "admin", "password": password.decode()}).encode(),
            headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(request, timeout=5) as response:
            assert "csrf_token" in json.load(response), "configured login failed"
    finally:
        process.terminate()
        process.wait(timeout=10)
print("Setup wizard: three prompts, hidden password, safe repeat, login and graceful shutdown passed.")

with tempfile.TemporaryDirectory() as directory:
    config = Path(directory) / "config.json"
    secret = "automation-test-password-123"
    result = subprocess.run([binary,"setup","--domain","localhost","--config",str(config),"--data-dir",directory,"--password-stdin"],input=secret+"\n",text=True,capture_output=True)
    assert result.returncode == 0, result.stderr
    assert secret not in result.stdout + result.stderr
    assert secret not in config.read_text()
print("Automated setup: stdin password stays out of argv, output and configuration plaintext.")

with tempfile.TemporaryDirectory() as directory:
    config = Path(directory) / "config.json"
    database = Path(directory) / "panel.sqlite"
    sentinel = b"existing-database-must-not-be-reinitialized"
    database.write_bytes(sentinel)
    result = subprocess.run([binary,"setup","--domain","localhost","--config",str(config),"--data-dir",directory,"--password-stdin"],input="automation-test-password-123\n",text=True,capture_output=True)
    assert result.returncode != 0, "setup adopted a database without its original key"
    assert database.read_bytes() == sentinel and not config.exists(), "orphan database/config was overwritten"
print("Setup recovery guard: existing database without its original config/key is preserved.")
