#!/usr/bin/env bash
# shellcheck disable=SC2154
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"

require_root
validate_inputs
check_host
require_command firewall-cmd

python_runner=""
for candidate in python3 python; do
  if command -v "${candidate}" >/dev/null 2>&1 &&
    "${candidate}" -c 'import json, subprocess, tempfile' >/dev/null 2>&1; then
    python_runner="${candidate}"
    break
  fi
done
[[ -n "${python_runner}" ]] || die "HOST_DEPENDENCY_UNAVAILABLE" "A supported Python interpreter is required to read firewalld configuration."

config_json() {
  STATE_DIR="${state_dir}" "${python_runner}" - <<'PY'
import json
import os
import shutil
import subprocess
import threading

try:
    string_types = (basestring,)
except NameError:
    string_types = (str,)

state_dir = os.environ["STATE_DIR"]
probe = subprocess.Popen(["firewall-cmd", "--state"], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
probe.communicate()
active = probe.returncode == 0
command = "firewall-cmd" if active else "firewall-offline-cmd"

def decode_output(value):
    if isinstance(value, bytes):
        return value.decode("utf-8", "replace")
    return value

def run(*args):
    process = subprocess.Popen([command] + list(args), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    stdout, _ = process.communicate()
    if process.returncode != 0:
        return ""
    return decode_output(stdout).strip()

def values(*args):
    return [item for item in run(*args).split() if item]

def run_many(commands):
    if not commands:
        return []
    results = [""] * len(commands)
    def worker(index, args):
        results[index] = run(*args)
    threads = [threading.Thread(target=worker, args=(index, args))
               for index, args in enumerate(commands)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    return results

def system_value(property_name, default):
    try:
        process = subprocess.Popen(
            ["systemctl", "show", "firewalld.service", "--property=" + property_name, "--value"],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
        )
        stdout, _ = process.communicate()
    except OSError:
        return default
    value = decode_output(stdout).strip() if process.returncode == 0 else ""
    return value or default

def find_command(name):
    if hasattr(shutil, "which"):
        return shutil.which(name) or ""
    for directory in os.environ.get("PATH", "").split(os.pathsep):
        candidate = os.path.join(directory, name)
        if os.path.isfile(candidate) and os.access(candidate, os.X_OK):
            return candidate
    return ""

firewall_binary = find_command("firewall-cmd")
install_dir = os.path.dirname(os.path.dirname(firewall_binary)) if firewall_binary else ""
runtime = {
    "port": os.environ.get("PANEL_PORT", "0"),
    "bindAddress": "system",
    "installDir": install_dir,
    "dataDir": "/etc/firewalld" if os.path.isdir("/etc/firewalld") else "",
    "logDir": "/var/log/firewalld" if os.path.isdir("/var/log/firewalld") else "journal",
    "runUser": system_value("User", "root"),
    "runGroup": system_value("Group", "root"),
}

def empty_zone(name):
    return {
        "name": name,
        "interfaces": [],
        "sources": [],
        "services": [],
        "ports": [],
        "protocols": [],
        "richRules": [],
        "masquerade": False,
    }

def assign_zone_value(zone, key, value):
    fields = {
        "interfaces": "interfaces",
        "sources": "sources",
        "services": "services",
        "ports": "ports",
        "protocols": "protocols",
        "masquerade": "masquerade",
        "rich rules": "richRules",
    }
    field = fields.get(key)
    if field is None:
        return
    if field == "masquerade":
        zone[field] = value == "yes"
    elif field == "richRules":
        zone[field] = []
    else:
        zone[field] = [item for item in value.split() if item]

def parse_all_zones(output):
    zones = []
    current = None
    rich_rules = False
    for raw_line in output.splitlines():
        if not raw_line.strip():
            continue
        if not raw_line.startswith((" ", "\t")):
            name = raw_line.strip().split(" ", 1)[0]
            current = empty_zone(name)
            zones.append(current)
            rich_rules = False
            continue
        if current is None:
            continue
        line = raw_line.strip()
        if ":" in line:
            key, value = line.split(":", 1)
            key = key.strip().lower()
            assign_zone_value(current, key, value.strip())
            rich_rules = key == "rich rules"
        elif rich_rules and line.startswith("rule "):
            current["richRules"].append(line)
    return zones

zone_output, default_zone_output, log_denied_output, direct_rules_output = run_many([
    ("--list-all-zones",),
    ("--get-default-zone",),
    ("--get-log-denied",),
    ("--direct", "--get-all-rules"),
])
zones = parse_all_zones(zone_output)
if not zones:
    # Older firewalld releases may not implement --list-all-zones. Keep a
    # compatibility fallback, but use one query per zone instead of the
    # previous seven queries per zone.
    zone_names = values("--get-zones")
    summaries = run_many([
        ("--zone=" + zone, "--list-all") for zone in zone_names
    ])
    for zone, summary in zip(zone_names, summaries):
        parsed = parse_all_zones(summary)
        if parsed:
            parsed[0]["name"] = zone
            zones.append(parsed[0])
        else:
            zones.append(empty_zone(zone))

managed = {"zones": [], "directRules": [], "icmpBlocks": [], "forwardPorts": []}
try:
    with open(os.path.join(state_dir, "managed-rules.json")) as handle:
        candidate = json.load(handle).get("managed", {})
        if isinstance(candidate, dict):
            managed.update(candidate)
except (OSError, ValueError, TypeError):
    pass

effective = {
    "defaultZone": default_zone_output or "public",
    "logDenied": log_denied_output or "off",
    "zones": zones,
    "directRules": [line for line in direct_rules_output.splitlines() if line],
    "runtime": runtime,
}
print(json.dumps({"managed": managed, "effective": effective}, ensure_ascii=True, separators=(",", ":"), sort_keys=True))
PY
}

configuration_revision() {
  printf '%s' "$1" | sha256sum | awk '{print $1}'
}

if [[ "${ONEINSTACK_CONFIG_OPERATION:-get}" == "get" ]]; then
  payload="$(config_json)"
  revision="$(configuration_revision "${payload}")"
  printf '%s' "${payload}" | "${python_runner}" -c '
import json
import sys

raw = sys.stdin.read().strip()
data = json.loads(raw)
effective = data["effective"]
runtime = effective["runtime"]
print("component=firewalld")
print("revision=" + sys.argv[1])
print("apply_mode=reload")
print("default-zone=" + effective.get("defaultZone", "public"))
print("log-denied=" + effective.get("logDenied", "off"))
for key in ("port", "bindAddress", "installDir", "dataDir", "logDir", "runUser", "runGroup"):
    print("runtime." + key + "=" + runtime.get(key, ""))
print("rules=" + raw)
' "${revision}"
  exit 0
fi

config_file="${ONEINSTACK_CONFIG_FILE:-}"
[[ -f "${config_file}" ]] || die "CONFIG_INVALID" "A structured firewalld configuration file is required."
if ! service_active && ! command -v firewall-offline-cmd >/dev/null 2>&1; then
  die "HOST_DEPENDENCY_UNAVAILABLE" "firewall-offline-cmd is required while firewalld is stopped."
fi
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
current_revision="$(configuration_revision "$(config_json)")"
[[ -n "${expected_revision}" && "${expected_revision}" == "${current_revision}" ]] ||
  die "CONFIG_REVISION_CONFLICT" "firewalld configuration changed after it was read."

backup_dir="$(mktemp -d)"
cleanup() { rm -rf -- "${backup_dir}"; }
restore_config() {
  if [[ -d "${backup_dir}/config" ]]; then
    rm -rf -- /etc/firewalld
    cp -a -- "${backup_dir}/config" /etc/firewalld
    if service_active; then firewall-cmd --reload >/dev/null 2>&1 || true; fi
  fi
  if [[ -f "${backup_dir}/managed-rules.json" ]]; then
    cp -a -- "${backup_dir}/managed-rules.json" "${managed_rules_file}"
  else
    rm -f -- "${managed_rules_file}"
  fi
}
trap 'status=$?; if [[ "$status" -ne 0 ]]; then restore_config || true; fi; cleanup; exit "$status"' EXIT
[[ -d /etc/firewalld ]] && cp -a -- /etc/firewalld "${backup_dir}/config"
[[ -f "${managed_rules_file}" ]] && cp -a -- "${managed_rules_file}" "${backup_dir}/managed-rules.json"

STATE_DIR="${state_dir}" CONFIG_FILE="${config_file}" "${python_runner}" - <<'PY'
import json
import os
import re
import socket
import subprocess
import tempfile

state_dir = os.environ["STATE_DIR"]
config_file = os.environ["CONFIG_FILE"]
with open(config_file) as handle:
    payload = json.load(handle)
if not isinstance(payload, dict) or set(payload) != {"default-zone", "log-denied", "rules"}:
    raise SystemExit("configuration fields are invalid")
zone_pattern = re.compile(r"[A-Za-z0-9_.-]{1,64}\Z")
try:
    string_types = (basestring,)
except NameError:
    string_types = (str,)

def fullmatch(pattern, value):
    return re.match(pattern, value) is not None

if not isinstance(payload["default-zone"], string_types) or not fullmatch(zone_pattern, payload["default-zone"]):
    raise SystemExit("default zone is invalid")
if payload["log-denied"] not in ("off", "unicast", "broadcast", "multicast", "all"):
    raise SystemExit("log-denied is invalid")
rules = payload["rules"]
if not isinstance(rules, dict) or set(rules) != {"managed", "effective"}:
    raise SystemExit("rules must contain managed and effective")
managed = rules["managed"]
if not isinstance(managed, dict) or set(managed) != {"zones", "directRules", "icmpBlocks", "forwardPorts"}:
    raise SystemExit("managed rules are invalid")
for key in ("zones", "directRules", "icmpBlocks", "forwardPorts"):
    if not isinstance(managed.get(key), list):
        raise SystemExit("managed field %s is invalid" % key)

protocol_pattern = re.compile(r"(tcp|udp|sctp|dccp)\Z")
port_pattern = re.compile(r"(?:[1-9][0-9]{0,4})(?:-(?:[1-9][0-9]{0,4}))?/(?:tcp|udp|sctp|dccp)\Z")
dangerous_pattern = re.compile(r"[\x00\r\n;$|&`]")
def valid_port(value):
    if not isinstance(value, string_types) or not fullmatch(port_pattern, value):
        return False
    values = value.split("/", 1)[0].split("-", 1)
    numbers = [int(item) for item in values]
    return all(1 <= number <= 65535 for number in numbers) and (len(numbers) == 1 or numbers[0] <= numbers[1])
def valid_address(value):
    try:
        import ipaddress
        ipaddress.ip_network(value, strict=False)
    except ImportError:
        try:
            address, prefix = value.split("/", 1)
            prefix = int(prefix)
            family = socket.AF_INET6 if ":" in address else socket.AF_INET
            socket.inet_pton(family, address)
            return 0 <= prefix <= (128 if family == socket.AF_INET6 else 32)
        except (ValueError, TypeError, socket.error):
            return False
    except (ValueError, TypeError):
        return False
    return True
def valid_rich(value):
    return isinstance(value, string_types) and 0 < len(value) <= 2048 and not dangerous_pattern.search(value)
def valid_direct(item):
    if not isinstance(item, dict):
        return False
    if item.get("ipv") not in ("ipv4", "ipv6") or not isinstance(item.get("table"), string_types) or not isinstance(item.get("chain"), string_types):
        return False
    if not fullmatch(re.compile(r"[A-Za-z0-9_.-]{1,32}\Z"), item["table"]) or not fullmatch(re.compile(r"[A-Za-z0-9_.-]{1,64}\Z"), item["chain"]):
        return False
    if not isinstance(item.get("priority"), int) or not -32768 <= item["priority"] <= 32767:
        return False
    args = item.get("args", [])
    return isinstance(args, list) and all(isinstance(arg, string_types) and 0 < len(arg) <= 256 and not dangerous_pattern.search(arg) for arg in args)

probe = subprocess.Popen(["firewall-cmd", "--state"], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
probe.communicate()
active = probe.returncode == 0
command = "firewall-cmd" if active else "firewall-offline-cmd"
prefix = ["--permanent"] if active else []
def call(args):
    process = subprocess.Popen([command] + prefix + list(args), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    stdout, stderr = process.communicate()
    if process.returncode != 0:
        message = stderr.decode("utf-8", "replace") if isinstance(stderr, bytes) else stderr
        raise SystemExit(message.strip() or "firewalld command failed")
def zone_args(zone, option, value):
    return ["--zone=%s" % zone, "--%s=%s" % (option, value)]

old = {"zones": [], "directRules": [], "icmpBlocks": [], "forwardPorts": []}
try:
    with open(os.path.join(state_dir, "managed-rules.json")) as handle:
        candidate = json.load(handle).get("managed", {})
        if isinstance(candidate, dict):
            old.update(candidate)
except (OSError, ValueError, TypeError):
    pass

def remove_zone_rules(zone):
    if not isinstance(zone, dict) or not fullmatch(zone_pattern, str(zone.get("name", ""))):
        return
    name = zone["name"]
    for service in zone.get("services", []):
        if isinstance(service, string_types): call(zone_args(name, "remove-service", service))
    for port in zone.get("ports", []):
        if isinstance(port, string_types): call(zone_args(name, "remove-port", port))
    for protocol in zone.get("protocols", []):
        if isinstance(protocol, string_types): call(zone_args(name, "remove-protocol", protocol))
    for source in zone.get("sources", []):
        if isinstance(source, string_types): call(zone_args(name, "remove-source", source))
    for interface in zone.get("interfaces", []):
        if isinstance(interface, string_types): call(zone_args(name, "remove-interface", interface))
    for rule in zone.get("richRules", []):
        if isinstance(rule, string_types): call(zone_args(name, "remove-rich-rule", rule))
    if zone.get("masquerade") is True: call(["--zone=%s" % name, "--remove-masquerade"])

for zone in old["zones"]: remove_zone_rules(zone)
for block in old["icmpBlocks"]:
    if isinstance(block, dict) and fullmatch(zone_pattern, str(block.get("zone", ""))) and isinstance(block.get("type"), string_types):
        call(zone_args(block["zone"], "remove-icmp-block", block["type"]))
for forward in old["forwardPorts"]:
    if isinstance(forward, dict):
        value = "port={}:proto={}:toport={}:toaddr={}".format(forward.get("port", ""), forward.get("protocol", ""), forward.get("toPort", ""), forward.get("toAddress", ""))
        call(["--remove-forward-port=%s" % value])
for direct in old["directRules"]:
    if isinstance(direct, dict) and valid_direct(direct):
        call(["--direct", "--remove-rule", direct["ipv"], direct["table"], direct["chain"], str(direct["priority"]), *direct.get("args", [])])

call(["--set-default-zone=%s" % payload["default-zone"]])
call(["--set-log-denied=%s" % payload["log-denied"]])
for zone in managed["zones"]:
    zone_keys = {"name", "interfaces", "sources", "services", "ports", "protocols", "richRules", "masquerade"}
    if not isinstance(zone, dict) or not set(zone).issubset(zone_keys) or not fullmatch(zone_pattern, str(zone.get("name", ""))): raise SystemExit("managed zone is invalid")
    for list_key in ("interfaces", "sources", "services", "ports", "protocols", "richRules"):
        if not isinstance(zone.get(list_key, []), list): raise SystemExit("managed zone list is invalid")
    if not isinstance(zone.get("masquerade", False), bool): raise SystemExit("managed masquerade value is invalid")
    name = zone["name"]
    for interface in zone.get("interfaces", []):
        if not isinstance(interface, string_types) or not fullmatch(re.compile(r"[A-Za-z0-9_.:-]{1,64}\Z"), interface): raise SystemExit("managed interface is invalid")
        call(zone_args(name, "add-interface", interface))
    for source in zone.get("sources", []):
        if not isinstance(source, string_types) or not valid_address(source): raise SystemExit("managed source is invalid")
        call(zone_args(name, "add-source", source))
    for service in zone.get("services", []):
        if not isinstance(service, string_types) or not fullmatch(re.compile(r"[A-Za-z0-9_.:-]{1,128}\Z"), service): raise SystemExit("managed service is invalid")
        call(zone_args(name, "add-service", service))
    for port in zone.get("ports", []):
        if not valid_port(port): raise SystemExit("managed port is invalid")
        call(zone_args(name, "add-port", port))
    for protocol in zone.get("protocols", []):
        if not isinstance(protocol, string_types) or not fullmatch(protocol_pattern, protocol): raise SystemExit("managed protocol is invalid")
        call(zone_args(name, "add-protocol", protocol))
    for rule in zone.get("richRules", []):
        if not valid_rich(rule): raise SystemExit("managed rich rule is invalid")
        call(zone_args(name, "add-rich-rule", rule))
    if zone.get("masquerade") is True: call(["--zone=%s" % name, "--add-masquerade"])
for block in managed["icmpBlocks"]:
    if not isinstance(block, dict) or set(block) != {"zone", "type"} or not fullmatch(zone_pattern, str(block.get("zone", ""))) or not fullmatch(re.compile(r"[A-Za-z0-9_.-]{1,64}\Z"), str(block.get("type", ""))): raise SystemExit("managed ICMP block is invalid")
    call(zone_args(block["zone"], "add-icmp-block", block["type"]))
for forward in managed["forwardPorts"]:
    if not isinstance(forward, dict) or set(forward) != {"port", "protocol", "toPort", "toAddress"}: raise SystemExit("managed forward port is invalid")
    port = str(forward.get("port", "")); protocol = str(forward.get("protocol", "")); target_port = str(forward.get("toPort", "")); target_address = str(forward.get("toAddress", ""))
    if not fullmatch(re.compile(r"[1-9][0-9]{0,4}(?:-[1-9][0-9]{0,4})?\Z"), port) or not fullmatch(protocol_pattern, protocol) or not fullmatch(re.compile(r"[1-9][0-9]{0,4}\Z"), target_port) or not valid_address(target_address): raise SystemExit("managed forward port is invalid")
    if any(int(item) > 65535 for item in port.split("-")) or int(target_port) > 65535: raise SystemExit("managed forward port is invalid")
    call(["--add-forward-port=port={}:proto={}:toport={}:toaddr={}".format(port, protocol, target_port, target_address)])
for direct in managed["directRules"]:
    if not valid_direct(direct) or set(direct) != {"ipv", "table", "chain", "priority", "args"}: raise SystemExit("managed direct rule is invalid")
    call(["--direct", "--add-rule", direct["ipv"], direct["table"], direct["chain"], str(direct["priority"]), *direct["args"]])

path = os.path.join(state_dir, "managed-rules.json")
if not os.path.isdir(state_dir):
    os.makedirs(state_dir, 488)
fd, temporary = tempfile.mkstemp(prefix=".managed-rules-", dir=state_dir)
with os.fdopen(fd, "w") as handle:
    json.dump({"managed": managed}, handle, ensure_ascii=True, separators=(",", ":"), sort_keys=True)
    handle.write("\n")
os.chmod(temporary, 384)
os.rename(temporary, path)
PY

if service_active; then
  firewall-cmd --reload || die "SERVICE_RELOAD_FAILED" "firewalld reload failed after configuration publish."
  firewall-cmd --state >/dev/null 2>&1 || die "SERVICE_RELOAD_FAILED" "firewalld is not ready after configuration reload."
fi
trap - EXIT
cleanup
emit_progress 100 config.apply.completed "firewalld managed configuration applied and verified"
