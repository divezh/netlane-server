"""Generate real traffic for the isolated Docker browser fixture; stdin carries a disposable profile."""
import base64
import glob
import json
import os
from pathlib import Path
import socket
import ssl
import sys

assert os.environ.get("TT_WEB_FIXTURE") == "1", "only run inside the disposable web fixture"
link=json.load(sys.stdin)["deep_link"]
assert link.startswith("tt://?")
raw=base64.urlsafe_b64decode(link[6:]+"="*(-len(link[6:])%4))
position,fields=0,{}
def integer():
    global position
    size=1<<(raw[position]>>6)
    value=int.from_bytes(raw[position:position+size],"big")&((1<<(size*8-2))-1)
    position+=size
    return value
while position<len(raw):
    tag,length=integer(),integer()
    fields[tag]=raw[position:position+length]
    position+=length
roots=glob.glob("/tmp/tt-web-*")
assert len(roots)==1
context=ssl.create_default_context(cafile=str(Path(roots[0])/"cert.pem"))
auth=base64.b64encode(fields[5]+b":"+fields[6]).decode()
with context.wrap_socket(socket.create_connection(("127.0.0.1",4443),5),server_hostname="localhost") as stream:
    stream.sendall(f"CONNECT 127.0.0.1:18089 HTTP/1.1\r\nHost: localhost\r\nProxy-Authorization: Basic {auth}\r\n\r\n".encode())
    header=b""
    while not header.endswith(b"\r\n\r\n"):
        part=stream.recv(1)
        assert part,"tunnel closed"
        header+=part
    assert b" 200 " in header.split(b"\r\n")[0]
    payload=b"q"*128
    stream.sendall(payload)
    received=b""
    while len(received)<len(payload):
        part=stream.recv(len(payload)-len(received))
        assert part,"echo closed"
        received+=part
    assert received==payload
print("256")
