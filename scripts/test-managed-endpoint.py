"""End-to-end panel -> Unix RPC -> real TLS tunnel, using disposable credentials only."""
import base64
from datetime import datetime, timezone, timedelta
import http.cookiejar
import json
import os
import pty
import select
import signal
import socket
import socketserver
import ssl
import subprocess
import sys
import tempfile
import termios
import threading
import time
import tomllib
import urllib.error
import urllib.request
from pathlib import Path

endpoint, panel = (str(Path(arg).resolve()) for arg in sys.argv[1:3])
assert "-admin-preview.5" in subprocess.check_output([endpoint,"--version"],text=True), "wrong endpoint build"
os.umask(0o077)


def free_port():
    with socket.socket() as stream:
        stream.bind(("127.0.0.1", 0))
        return stream.getsockname()[1]


def eventually(action, timeout=10):
    deadline = time.monotonic() + timeout
    while True:
        try:
            return action()
        except (OSError, AssertionError, ValueError):
            if time.monotonic() > deadline:
                raise
            time.sleep(0.05)


class Echo(socketserver.BaseRequestHandler):
    def handle(self):
        while data := self.request.recv(16384):
            self.request.sendall(data)


with tempfile.TemporaryDirectory(prefix="tt-managed-") as temporary:
    root = Path(temporary)
    socket_path, state_path = root / "control.sock", root / "managed.json"
    vpn_port, api_port = free_port(), free_port()
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                    "-keyout", str(root / "key.pem"), "-out", str(root / "cert.pem"),
                    "-days", "1", "-subj", "/CN=localhost"], check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    (root / "credentials.toml").write_text('[[client]]\nusername="bootstrap"\npassword="bootstrap-test-password"\n')
    (root / "vpn.toml").write_text(
        f'listen_address="127.0.0.1:{vpn_port}"\ncredentials_file={json.dumps(str(root / "credentials.toml"))}\n'
        'allow_private_network_connections=true\n[listen_protocols.http1]\n[listen_protocols.http2]\n[listen_protocols.quic]\n')
    (root / "hosts.toml").write_text(
        '[[main_hosts]]\nhostname="localhost"\n'
        f'cert_chain_path={json.dumps(str(root / "cert.pem"))}\nprivate_key_path={json.dumps(str(root / "key.pem"))}\n')

    def start_endpoint():
        process = subprocess.Popen([endpoint, str(root / "vpn.toml"), str(root / "hosts.toml"),
                                    "--control-socket", str(socket_path), "--control-state", str(state_path)],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        return process

    def rpc(request):
        with socket.socket(socket.AF_UNIX) as stream:
            stream.settimeout(5)
            stream.connect(str(socket_path))
            stream.sendall(json.dumps(request).encode() + b"\n")
            with stream.makefile("rb") as response:
                return json.loads(response.readline(65537))

    endpoint_process = start_endpoint()
    panel_process = None
    echo = socketserver.ThreadingTCPServer(("127.0.0.1", 0), Echo)
    echo.daemon_threads = True
    threading.Thread(target=echo.serve_forever, daemon=True).start()
    streams = []
    try:
        initial = eventually(lambda: rpc({"op": "status"}))
        assert initial["ok"] and initial["state"]["generation"] == 0
        assert socket_path.stat().st_mode & 0o777 == 0o660
        assert state_path.stat().st_mode & 0o777 == 0o600
        assert "password" not in json.dumps(initial)
        assert rpc({"op": "unknown"}) == {"ok": False, "error": "invalid_request"}

        config = root / "panel.json"
        password = b"disposable-admin-password-123"
        pid, terminal = pty.fork()
        if pid == 0:
            os.execv(panel, [panel, "setup", "--domain", "localhost", "--config", str(config), "--data-dir", str(root)])
        pending = [("символов): ".encode(), password), ("Повторите пароль: ".encode(), password)]
        transcript = b""
        deadline = time.monotonic() + 15
        reaped = False
        try:
            while pending:
                assert time.monotonic() < deadline, "setup timed out"
                if select.select([terminal], [], [], 0.1)[0]:
                    transcript += os.read(terminal, 8192)
                if pending[0][0] in transcript and not termios.tcgetattr(terminal)[3] & termios.ECHO:
                    os.write(terminal, pending.pop(0)[1] + b"\n")
            _, status = os.waitpid(pid, 0)
            reaped = True
            assert os.waitstatus_to_exitcode(status) == 0
        finally:
            os.close(terminal)
            if not reaped:
                os.kill(pid, signal.SIGKILL)
                os.waitpid(pid, 0)
        cfg = json.loads(config.read_text())
        cfg.update(listen=f"127.0.0.1:{api_port}", control_socket=str(socket_path))
        config.write_text(json.dumps(cfg))
        panel_process = subprocess.Popen([panel, "serve", "--config", str(config)], stdout=subprocess.DEVNULL)
        base = f"http://127.0.0.1:{api_port}"
        opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
        csrf = ""

        def api(method, path, data=None, expected=200):
            request = urllib.request.Request(base + path, method=method,
                data=None if data is None else json.dumps(data).encode(),
                headers={"Content-Type": "application/json", "X-CSRF-Token": csrf})
            try:
                response = opener.open(request, timeout=10)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                content = json.load(response)
                assert response.status == expected, (path, response.status, content)
                return content

        eventually(lambda: api("GET", "/healthz"))
        csrf = api("POST", "/api/v1/auth/login", {"username": "admin", "password": password.decode()})["csrf_token"]
        assert api("GET", "/api/v1/status")["capabilities"]["speed_limits"]
        assert api("GET", "/api/v1/auth/session")["csrf_token"] == csrf
        users = {}
        policy = {"enabled": True, "download_mbps": 2, "upload_mbps": 1, "quota_bytes": 0}
        for username in ("alice", "bob"):
            user = api("POST", "/api/v1/users", {"username": username, "password": "test-vpn-password-123", "group_id": "all", "policy": policy}, 201)["user"]
            users[username] = user
            assert api("POST", f'/api/v1/users/{user["id"]}/apply', {})["vpn_applied"]

        def decode_link(link):
            raw = base64.urlsafe_b64decode(link.removeprefix("tt://?") + "=" * (-len(link.removeprefix("tt://?")) % 4))
            position, fields = 0, {}
            def integer():
                nonlocal position
                length = 1 << (raw[position] >> 6)
                value = int.from_bytes(raw[position:position+length], "big") & ((1 << (length*8-2))-1)
                position += length
                return value
            while position < len(raw):
                tag, length = integer(), integer()
                fields[tag] = raw[position:position+length]
                position += length
            assert position == len(raw)
            return fields

        for protocol, tag in (("http2", b"\x01"), ("http3", b"\x02")):
            profile = api("POST", f'/api/v1/users/{users["alice"]["id"]}/profile', {"protocol": protocol, "anti_dpi": True})
            fields = decode_link(profile["deep_link"])
            assert fields[0] == b"\x01" and fields[1] == b"localhost" and fields[2] == b"localhost:443"
            assert fields[4] == b"\x00" and fields[5] == b"alice" and fields[6] == b"test-vpn-password-123"
            assert fields[9] == tag and fields[10] == b"\x01" and 7 not in fields
            assert f'upstream_protocol = "{protocol}"' in profile["toml"] and "skip_verification = false" in profile["toml"]
            assert tomllib.loads(profile["toml"])["password"] == "test-vpn-password-123"
        events = api("GET", "/api/v1/audit")["events"]
        assert sum(e["action"] == "user.profile_exported" for e in events) == 2
        assert "test-vpn-password-123" not in json.dumps(events)
        special_password = 'x'*64 + '\\"\n\x01кириллица'
        special = api("POST", "/api/v1/users", {"username": "z-special", "password": special_password, "group_id": "all", "policy": policy}, 201)["user"]
        assert api("POST", f'/api/v1/users/{special["id"]}/apply', {})["vpn_applied"]
        special_profile = api("POST", f'/api/v1/users/{special["id"]}/profile', {"protocol": "http2"})
        assert decode_link(special_profile["deep_link"])[6].decode() == special_password
        assert tomllib.loads(special_profile["toml"])["password"] == special_password

        def connect(username, accepted=True):
            stream = ssl._create_unverified_context().wrap_socket(socket.create_connection(("127.0.0.1", vpn_port), 5), server_hostname="localhost")
            streams.append(stream)
            target = f"127.0.0.1:{echo.server_address[1]}"
            credentials = base64.b64encode(f"{username}:test-vpn-password-123".encode()).decode()
            stream.sendall(f"CONNECT {target} HTTP/1.1\r\nHost: localhost\r\nProxy-Authorization: Basic {credentials}\r\n\r\n".encode())
            header = b""
            while not header.endswith(b"\r\n\r\n"):
                piece = stream.recv(1)
                assert piece, "endpoint closed before CONNECT response"
                header += piece
            assert (b" 200 " in header.split(b"\r\n")[0]) == accepted, header
            return stream

        def echo_ok(stream):
            stream.sendall(b"vpn-check")
            assert stream.recv(9) == b"vpn-check"

        def closed(stream):
            try:
                assert stream.recv(1) == b""
            except (ssl.SSLError, ConnectionResetError, BrokenPipeError):
                pass

        alice, bob = connect("alice"), connect("bob")
        echo_ok(alice)
        echo_ok(bob)
        assert api("POST", f'/api/v1/users/{users["alice"]["id"]}/disconnect', {})["disconnect_requested"] == 1
        closed(alice)
        echo_ok(bob)
        alice = connect("alice")
        echo_ok(alice)
        disabled = dict(policy, enabled=False, quota_bytes=1000000000)
        user = users["alice"]
        api("PUT", f'/api/v1/users/{user["id"]}', {"expected_revision": 1, "policy": disabled})
        assert api("POST", f'/api/v1/users/{user["id"]}/apply', {})["applied_revision"] == 2
        api("POST", f'/api/v1/users/{user["id"]}/profile', {"protocol": "http2"}, 409)
        closed(alice)
        connect("alice", accepted=False)
        echo_ok(bob)
        generation = rpc({"op": "status"})["state"]["generation"]
        # Applying an acknowledged revision again is idempotent and keeps other sessions alive.
        api("POST", f'/api/v1/users/{users["bob"]["id"]}/apply', {})
        assert rpc({"op": "status"})["state"]["generation"] == generation
        echo_ok(bob)
        assert api("GET", "/api/v1/status")["capabilities"]["durable_quotas"]
        quota_policy=dict(policy,quota_bytes=18)
        quota_user = api("POST", "/api/v1/users", {"username": "quota", "password":"test-vpn-password-123", "group_id": "all", "policy": quota_policy}, 201)["user"]
        api("POST", f'/api/v1/users/{quota_user["id"]}/apply', {})
        limited=connect("quota");echo_ok(limited);closed(limited);connect("quota",accepted=False)
        assert api("POST", f'/api/v1/users/{quota_user["id"]}/profile', {"protocol":"http2"},409)["error"]=="user_quota_exhausted"
        usage=api("GET","/api/v1/usage")
        entry=next(r for r in usage["records"] if r["user_id"]==quota_user["id"])
        assert entry["charged_bytes"]==18 and entry["upload_bytes"]==entry["download_bytes"]==9
        quota_policy["quota_bytes"]=36
        api("PUT",f'/api/v1/users/{quota_user["id"]}',{"expected_revision":1,"policy":quota_policy})
        api("POST", f'/api/v1/users/{quota_user["id"]}/apply', {})
        limited=connect("quota");echo_ok(limited);closed(limited);connect("quota",accepted=False)
        assert next(r for r in api("GET","/api/v1/usage")["records"] if r["user_id"]==quota_user["id"])["charged_bytes"]==36
        quota_policy["quota_bytes"]=1048576+54
        api("PUT",f'/api/v1/users/{quota_user["id"]}',{"expected_revision":2,"policy":quota_policy})
        api("POST", f'/api/v1/users/{quota_user["id"]}/apply', {})
        echo_ok(connect("quota"))
        generation=rpc({"op":"status"})["state"]["generation"]
        endpoint_process.kill()
        endpoint_process.wait(timeout=10)
        for stream in streams:stream.close()
        streams.clear()
        endpoint_process=start_endpoint()
        eventually(lambda:rpc({"op":"status"}))
        entry=next(r for r in api("GET","/api/v1/usage")["records"] if r["user_id"]==quota_user["id"])
        assert 54<=entry["charged_bytes"]<=1048576+54
        quota_policy["quota_bytes"]=entry["charged_bytes"]+18
        api("PUT",f'/api/v1/users/{quota_user["id"]}',{"expected_revision":3,"policy":quota_policy})
        api("POST",f'/api/v1/users/{quota_user["id"]}/apply',{})
        limited=connect("quota");echo_ok(limited);closed(limited);connect("quota",accepted=False)
        echo_ok(connect("bob"))
        generation=rpc({"op":"status"})["state"]["generation"]
        for stream in streams:
            stream.close()
        streams.clear()
        endpoint_process.terminate()
        endpoint_process.wait(timeout=10)
        endpoint_process = start_endpoint()
        restored = eventually(lambda: rpc({"op": "status"}))
        assert restored["state"]["generation"] == generation
        assert next(user for user in restored["state"]["users"] if user["username"] == "alice")["enabled"] is False
        echo_ok(connect("bob"))
        connect("alice", accepted=False)
        assert api("GET", "/api/v1/users")["users"][0]["applied_revision"] == 2

        assert api("GET", "/api/v1/status")["capabilities"]["expiry"]
        original_deadline = datetime.now(timezone.utc) + timedelta(seconds=1.5)
        expiring_policy = dict(policy, expires_at=original_deadline.isoformat())
        expiring = api("POST", "/api/v1/users", {"username": "zz-expiry", "password": "test-vpn-password-123", "group_id": "all", "policy": expiring_policy}, 201)["user"]
        assert api("POST", f'/api/v1/users/{expiring["id"]}/apply', {})["vpn_applied"]
        expiring_stream = connect("zz-expiry")
        echo_ok(expiring_stream)
        extended_deadline = original_deadline + timedelta(seconds=1)
        expiring_policy["expires_at"] = extended_deadline.isoformat()
        api("PUT", f'/api/v1/users/{expiring["id"]}', {"expected_revision": 1, "policy": expiring_policy})
        assert api("POST", f'/api/v1/users/{expiring["id"]}/apply', {})["applied_revision"] == 2
        # Stopping the panel must neither disable the endpoint timer nor reset it.
        panel_process.terminate()
        panel_process.wait(timeout=10)
        time.sleep(max(0, original_deadline.timestamp()-time.time()+0.1))
        echo_ok(expiring_stream)
        closed(expiring_stream)
        connect("zz-expiry", accepted=False)
        echo_ok(connect("bob"))
        for stream in streams:
            stream.close()
        streams.clear()
        endpoint_process.terminate()
        endpoint_process.wait(timeout=10)
        endpoint_process = start_endpoint()
        restored_expiry = eventually(lambda: rpc({"op": "status"}))
        expired = next(u for u in restored_expiry["state"]["users"] if u["id"] == expiring["id"])
        assert expired["expired"] and expired["expires_at"] == expiring_policy["expires_at"]
        connect("zz-expiry", accepted=False)
        panel_process = subprocess.Popen([panel, "serve", "--config", str(config)], stdout=subprocess.DEVNULL)
        eventually(lambda: api("GET", "/healthz"))
        csrf = api("POST", "/api/v1/auth/login", {"username": "admin", "password": password.decode()})["csrf_token"]
        assert api("POST", f'/api/v1/users/{expiring["id"]}/profile', {"protocol": "http2"}, 409)["error"] == "user_expired"
        api("POST", f'/api/v1/users/{expiring["id"]}/apply', {})
        connect("zz-expiry", accepted=False)
        expiring_policy["expires_at"] = ""
        api("PUT", f'/api/v1/users/{expiring["id"]}', {"expected_revision": 2, "policy": expiring_policy})
        assert api("POST", f'/api/v1/users/{expiring["id"]}/apply', {})["applied_revision"] == 3
        echo_ok(connect("zz-expiry"))

        assert api("GET", "/api/v1/status")["capabilities"]["remove_user"]
        victim=api("POST","/api/v1/users",{"username":"delete-me","password":"test-vpn-password-123","group_id":"all","policy":policy},201)["user"]
        api("POST",f'/api/v1/users/{victim["id"]}/apply',{})
        victim_stream=connect("delete-me");echo_ok(victim_stream)
        used_before=next(r for r in api("GET","/api/v1/usage")["records"] if r["user_id"]==victim["id"])["charged_bytes"]
        assert used_before>0
        api("DELETE",f'/api/v1/users/{victim["id"]}',{"expected_revision":2},409)
        echo_ok(victim_stream)
        state=rpc({"op":"status"})["state"]
        assert rpc({"op":"remove","expected_generation":state["generation"]+1,"id":victim["id"],"username":"delete-me","expected_revision":1})["error"]=="generation_conflict"
        assert rpc({"op":"remove","expected_generation":state["generation"],"id":victim["id"],"username":"delete-me","expected_revision":2})["error"]=="revision_conflict"
        assert api("DELETE",f'/api/v1/users/{victim["id"]}',{"expected_revision":1})["deleted"]
        closed(victim_stream);connect("delete-me",accepted=False);echo_ok(connect("bob"))
        assert all(u["id"]!=victim["id"] for u in api("GET","/api/v1/users")["users"])
        assert all(u["id"]!=victim["id"] for u in rpc({"op":"status"})["state"]["users"])
        assert next(r for r in api("GET","/api/v1/usage")["records"] if r["user_id"]==victim["id"])["charged_bytes"]>=used_before
        assert any(e["action"]=="user.deleted" and e["target"]==victim["id"] for e in api("GET","/api/v1/audit")["events"])
        api("POST",f'/api/v1/users/{victim["id"]}/profile',{"protocol":"http2"},404)
        # An idempotent retry must not touch a newly created user with the same name.
        replacement=api("POST","/api/v1/users",{"username":"delete-me","password":"test-vpn-password-123","group_id":"all","policy":policy},201)["user"]
        assert replacement["id"]!=victim["id"]
        api("POST",f'/api/v1/users/{replacement["id"]}/apply',{})
        replacement_stream=connect("delete-me");echo_ok(replacement_stream)
        state=rpc({"op":"status"})["state"]
        assert rpc({"op":"remove","expected_generation":state["generation"],"id":victim["id"],"username":"delete-me","expected_revision":1})["ok"]
        echo_ok(replacement_stream)
        api("DELETE",f'/api/v1/users/{replacement["id"]}',{"expected_revision":1})
        endpoint_process.terminate();endpoint_process.wait(timeout=10)
        endpoint_process=start_endpoint();eventually(lambda:rpc({"op":"status"}))
        connect("delete-me",accepted=False);echo_ok(connect("bob"))

        endpoint_process.terminate()
        endpoint_process.wait(timeout=10)
        usage_path=state_path.with_suffix(".usage.jsonl")
        saved_usage=usage_path.read_bytes()
        usage_path.unlink()
        endpoint_process=start_endpoint()
        assert endpoint_process.wait(timeout=10)!=0,"missing required usage journal silently reset quotas"
        usage_path.write_bytes(saved_usage[:-1])
        endpoint_process=start_endpoint()
        assert endpoint_process.wait(timeout=10)!=0,"torn usage journal silently accepted"
        usage_path.write_bytes(saved_usage)
        state_path.write_text("{corrupted")
        endpoint_process = start_endpoint()
        assert endpoint_process.wait(timeout=10) != 0, "corrupt managed state silently ignored"
    finally:
        for stream in streams:
            stream.close()
        for process in (panel_process, endpoint_process):
            if process is not None and process.poll() is None:
                process.terminate()
                process.wait(timeout=10)
        echo.shutdown()
        echo.server_close()
print("Managed endpoint E2E passed: users, profiles, expiry, shared quota, limit changes without reset, SIGKILL recovery, durable history, confirmed deletion, name reuse, missing/torn journal refusal and corrupt-state refusal.")
