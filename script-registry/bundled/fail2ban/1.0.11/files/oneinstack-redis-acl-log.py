#!/usr/bin/env python3
import fcntl
import ipaddress
import json
import os
import re
import shlex
import socket
import sys
import time
from datetime import datetime, timezone


CONFIG_CANDIDATES = (
    "/usr/local/redis/etc/redis.conf",
    "/etc/redis/redis.conf",
    "/etc/redis/redis-server.conf",
)
LOG_PATH = "/var/lib/oneinstack/fail2ban/redis-auth.log"
STATE_PATH = "/var/lib/oneinstack/fail2ban/redis-acl-log.state"
LOCK_PATH = "/var/lib/oneinstack/fail2ban/redis-acl-log.lock"
POLL_SECONDS = max(1, int(os.environ.get("ONEINSTACK_REDIS_ACL_POLL_SECONDS", "5")))


def config_path():
    for candidate in CONFIG_CANDIDATES:
        if os.path.isfile(candidate):
            return candidate
    return ""


def redis_config(path):
    values = {}
    try:
        with open(path, "r", encoding="utf-8", errors="replace") as stream:
            for raw_line in stream:
                line = raw_line.strip()
                if not line or line.startswith("#"):
                    continue
                try:
                    parts = shlex.split(line, comments=True, posix=True)
                except ValueError:
                    continue
                if len(parts) < 2:
                    continue
                key = parts[0].lower()
                if key not in values:
                    values[key] = " ".join(parts[1:])
    except OSError:
        return {}
    return values


def integer_option(values, name, default):
    try:
        value = int(values.get(name, default))
        return value if 1 <= value <= 65535 else default
    except (TypeError, ValueError):
        return default


def endpoint(values):
    unix_socket = values.get("unixsocket", "").strip()
    if unix_socket and os.path.exists(unix_socket):
        return "unix", unix_socket
    bind_hosts = values.get("bind", "127.0.0.1").split()
    host = next((item for item in bind_hosts if item not in ("0.0.0.0", "::")), "127.0.0.1")
    if host == "127.0.0.1" and "127.0.0.1" not in bind_hosts and "::1" in bind_hosts:
        host = "::1"
    return "tcp", (host, integer_option(values, "port", 6379))


def recv_line(stream):
    line = bytearray()
    while True:
        char = stream.recv(1)
        if not char:
            raise ConnectionError("Redis closed the connection")
        line.extend(char)
        if line.endswith(b"\r\n"):
            return bytes(line[:-2])


def read_resp(stream):
    prefix = stream.recv(1)
    if not prefix:
        raise ConnectionError("Redis closed the connection")
    if prefix == b"+":
        return recv_line(stream).decode("utf-8", "replace")
    if prefix == b"-":
        raise RuntimeError(recv_line(stream).decode("utf-8", "replace"))
    if prefix == b":":
        return int(recv_line(stream))
    if prefix == b"$":
        length = int(recv_line(stream))
        if length < 0:
            return None
        data = bytearray()
        while len(data) < length:
            chunk = stream.recv(length - len(data))
            if not chunk:
                raise ConnectionError("Redis closed the connection")
            data.extend(chunk)
        if stream.recv(2) != b"\r\n":
            raise ConnectionError("Invalid Redis bulk response")
        return bytes(data).decode("utf-8", "replace")
    if prefix in (b"*", b">", b"~"):
        length = int(recv_line(stream))
        if length < 0:
            return None
        return [read_resp(stream) for _ in range(length)]
    if prefix == b"_":
        recv_line(stream)
        return None
    raise RuntimeError("Unsupported Redis response type")


def command_parts(*parts):
    payload = [f"*{len(parts)}\r\n".encode()]
    for part in parts:
        value = str(part).encode("utf-8")
        payload.append(f"${len(value)}\r\n".encode())
        payload.append(value)
        payload.append(b"\r\n")
    return b"".join(payload)


def acl_log(values):
    kind, address = endpoint(values)
    password = values.get("requirepass", "").strip()
    if kind == "unix":
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(10)
        sock.connect(address)
    else:
        sock = socket.create_connection(address, timeout=10)
    with sock:
        if password:
            sock.sendall(command_parts("AUTH", password))
            read_resp(sock)
        sock.sendall(command_parts("ACL", "LOG", "128"))
        result = read_resp(sock)
        return result if isinstance(result, list) else []


def load_state():
    try:
        with open(STATE_PATH, "r", encoding="utf-8") as stream:
            value = json.load(stream)
            return value if isinstance(value, dict) else {}
    except (OSError, ValueError):
        return {}


def save_state(state):
    temporary = STATE_PATH + ".new"
    with open(temporary, "w", encoding="utf-8") as stream:
        json.dump(state, stream, separators=(",", ":"))
        stream.write("\n")
    os.chmod(temporary, 0o640)
    os.replace(temporary, STATE_PATH)


def entry_map(entry):
    if not isinstance(entry, list):
        return {}
    result = {}
    for index in range(0, len(entry) - 1, 2):
        key = str(entry[index])
        result[key] = entry[index + 1]
    return result


def client_ip(client_info):
    match = re.search(r"(?:^|\s)addr=([^\s]+)", str(client_info or ""))
    if not match:
        return ""
    value = match.group(1).strip("[]")
    candidates = [value]
    if ":" in value:
        candidates.append(value.rsplit(":", 1)[0].strip("[]"))
    for candidate in candidates:
        try:
            return str(ipaddress.ip_address(candidate))
        except ValueError:
            continue
    return ""


def event_key(entry):
    entry_id = entry.get("entry-id")
    timestamp = entry.get("timestamp-created")
    if entry_id is not None or timestamp is not None:
        return f"{entry_id}:{timestamp}"
    return "|".join(str(entry.get(name, "")) for name in ("reason", "object", "cinfo"))


def emit_events(entries, state):
    os.makedirs(os.path.dirname(LOG_PATH), mode=0o750, exist_ok=True)
    previous = state.setdefault("entries", {})
    changed = False
    with open(LOG_PATH, "a", encoding="utf-8") as log:
        os.chmod(LOG_PATH, 0o640)
        for raw_entry in entries:
            entry = entry_map(raw_entry)
            if entry.get("reason") != "auth":
                continue
            ip = client_ip(entry.get("cinfo"))
            if not ip:
                continue
            key = event_key(entry)
            count = int(entry.get("count") or 1)
            updated = str(entry.get("timestamp-last-updated") or entry.get("timestamp-created") or "")
            current = {"count": count, "updated": updated}
            if previous.get(key) == current:
                continue
            timestamp = datetime.now(timezone.utc).isoformat(timespec="seconds")
            log.write(f"{timestamp} Redis ACL authentication failure from {ip}\n")
            previous[key] = current
            changed = True
    if len(previous) > 1024:
        keys = list(previous.keys())[-1024:]
        state["entries"] = {key: previous[key] for key in keys}
        changed = True
    return changed


def run():
    os.umask(0o027)
    os.makedirs(os.path.dirname(LOG_PATH), mode=0o750, exist_ok=True)
    with open(LOG_PATH, "a", encoding="utf-8"):
        pass
    os.chmod(LOG_PATH, 0o640)
    with open(LOCK_PATH, "a+", encoding="utf-8") as lock:
        try:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return 0
        while True:
            path = config_path()
            if path:
                values = redis_config(path)
                try:
                    entries = acl_log(values)
                    state = load_state()
                    if emit_events(entries, state):
                        save_state(state)
                except (ConnectionError, OSError, RuntimeError, ValueError):
                    pass
            time.sleep(POLL_SECONDS)


if __name__ == "__main__":
    try:
        sys.exit(run())
    except KeyboardInterrupt:
        sys.exit(0)
