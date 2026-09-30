#!/usr/bin/env python3
import ipaddress
import json
import os
import re
import sys
import time

EVENT_FILE = "/var/lib/oneinstack/fail2ban/events.jsonl"
JAIL = re.compile(r"^oneinstack-[a-z0-9-]{1,80}-detect$")


def main() -> int:
    if len(sys.argv) != 4 or not JAIL.fullmatch(sys.argv[1]):
        return 2
    try:
        address = str(ipaddress.ip_address(sys.argv[2]))
        failures = int(sys.argv[3])
    except (ValueError, TypeError):
        return 2
    if failures < 0 or failures > 1000000:
        return 2
    event = {
        "jail": sys.argv[1],
        "ip": address,
        "failures": failures,
        "observedAt": int(time.time()),
    }
    line = (json.dumps(event, separators=(",", ":"), sort_keys=True) + "\n").encode()
    descriptor = os.open(EVENT_FILE, os.O_WRONLY | os.O_APPEND | os.O_CREAT | os.O_CLOEXEC, 0o640)
    try:
        os.write(descriptor, line)
    finally:
        os.close(descriptor)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
