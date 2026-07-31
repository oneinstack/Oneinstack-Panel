#!/usr/bin/env bash

set -euo pipefail

readonly SCRIPT_VERSION="3.0.0"
readonly MANAGED_MARKER="# Managed by OneinStack Panel installer"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
action="install"
root_prefix="${ONEINSTACK_INSTALL_ROOT:-}"
install_dir_runtime="/usr/local/one"
service_file_runtime="/etc/systemd/system/one.service"
update_service_file_runtime="/etc/systemd/system/one-update.service"
network_recovery_service_file_runtime="/etc/systemd/system/one-network-recover.service"
panel_restore_service_file_runtime="/etc/systemd/system/one-panel-restore.service"
link_path_runtime="/usr/local/bin/one"
binary_source="${script_dir}/one"
config_source="${script_dir}/config.yaml"
bundled_scripts_source="${script_dir}/script-registry/bundled"
admin_user="admin"
admin_password_file=""
health_url="http://127.0.0.1:8089/health/ready"
health_timeout=30
force=false
replace_config=false
skip_init=false
no_start=false
no_enable=false
no_health_check=false
allow_unsupported=false
purge=false
assume_yes=false
temporary_password_file=""
terminal_user_runtime="one-terminal"
terminal_home_runtime="/var/lib/one-terminal"

usage() {
  cat <<'EOF'
OneinStack Panel 安装与卸载工具

用法:
  ./install.sh [install] [选项]
  ./install.sh uninstall [选项]

安装选项:
  --binary PATH              发布包内的 one 二进制文件
  --config PATH              配置模板
  --install-dir PATH         安装目录，默认 /usr/local/one
  --admin-user USER          初始管理员用户名，默认 admin
  --admin-password-file PATH 从权限受控的文件读取初始密码
  --force                    更新已安装的程序文件
  --replace-config           同时使用模板替换现有配置
  --skip-init                跳过初始管理员创建
  --no-enable                不启用 systemd 开机启动
  --no-start                 不启动服务
  --no-health-check          不检查 /health/ready
  --health-url URL           自定义就绪检查地址
  --health-timeout SECONDS   就绪检查超时，默认 30 秒
  --allow-unsupported        允许在未验证的 Linux 发行版安装

卸载选项:
  --purge                    同时删除配置、数据库、日志和备份
  --yes                      与 --purge 一起使用，确认不可恢复删除

测试选项:
  --root PATH                将所有文件写入测试根目录，不调用 systemd

说明:
  安装脚本不会修改软件源、防火墙、内核参数或执行系统全量升级。
  普通卸载会保留安装目录内的配置、数据库、日志与备份。
EOF
}

log() {
  printf '[OneinStack] %s\n' "$*"
}

warn() {
  printf '[OneinStack] 警告: %s\n' "$*" >&2
}

die() {
  printf '[OneinStack] 错误: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  if [[ -n "$temporary_password_file" && -f "$temporary_password_file" ]]; then
    rm -f -- "$temporary_password_file"
  fi
}
trap cleanup EXIT

require_value() {
  local option="$1"
  local value="${2:-}"
  [[ -n "$value" ]] || die "${option} 需要参数"
}

validate_absolute_path() {
  local label="$1"
  local value="$2"
  [[ "$value" == /* ]] || die "${label} 必须是绝对路径: ${value}"
  [[ "$value" =~ ^/[A-Za-z0-9._/-]+$ ]] || die "${label} 包含不支持的字符: ${value}"
  [[ "$value" != *"/../"* && "$value" != */.. && "$value" != *"/./"* ]] ||
    die "${label} 不能包含 . 或 .. 路径段"
}

validate_install_dir() {
  validate_absolute_path "安装目录" "$install_dir_runtime"
  case "${install_dir_runtime%/}" in
    ""|/|/bin|/boot|/data|/dev|/etc|/home|/lib|/lib64|/opt|/root|/run|/sbin|/srv|/tmp|/usr|/usr/local|/var)
      die "拒绝使用过宽的安装目录: ${install_dir_runtime}"
      ;;
  esac
}

rooted_path() {
  local runtime_path="$1"
  printf '%s%s' "$root_prefix" "$runtime_path"
}

parse_arguments() {
  if [[ $# -gt 0 ]]; then
    case "$1" in
      install|uninstall)
        action="$1"
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
    esac
  fi

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --binary)
        require_value "$1" "${2:-}"
        binary_source="$2"
        shift 2
        ;;
      --config)
        require_value "$1" "${2:-}"
        config_source="$2"
        shift 2
        ;;
      --install-dir)
        require_value "$1" "${2:-}"
        install_dir_runtime="${2%/}"
        shift 2
        ;;
      --admin-user)
        require_value "$1" "${2:-}"
        admin_user="$2"
        shift 2
        ;;
      --admin-password-file)
        require_value "$1" "${2:-}"
        admin_password_file="$2"
        shift 2
        ;;
      --health-url)
        require_value "$1" "${2:-}"
        health_url="$2"
        shift 2
        ;;
      --health-timeout)
        require_value "$1" "${2:-}"
        health_timeout="$2"
        shift 2
        ;;
      --root)
        require_value "$1" "${2:-}"
        root_prefix="${2%/}"
        shift 2
        ;;
      --force)
        force=true
        shift
        ;;
      --replace-config)
        replace_config=true
        shift
        ;;
      --skip-init)
        skip_init=true
        shift
        ;;
      --no-start)
        no_start=true
        shift
        ;;
      --no-enable)
        no_enable=true
        shift
        ;;
      --no-health-check)
        no_health_check=true
        shift
        ;;
      --allow-unsupported)
        allow_unsupported=true
        shift
        ;;
      --purge)
        purge=true
        shift
        ;;
      --yes)
        assume_yes=true
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        die "未知参数: $1"
        ;;
    esac
  done
}

prepare_paths() {
  validate_install_dir
  [[ "$health_timeout" =~ ^[1-9][0-9]*$ ]] || die "健康检查超时必须是正整数"

  if [[ -n "$root_prefix" ]]; then
    validate_absolute_path "测试根目录" "$root_prefix"
    [[ "$root_prefix" != "/" ]] || die "测试根目录不能是 /"
    mkdir -p -- "$root_prefix"
    root_prefix="$(cd "$root_prefix" && pwd -P)"
    no_start=true
    no_enable=true
    no_health_check=true
    skip_init=true
  fi

  install_dir="$(rooted_path "$install_dir_runtime")"
  service_file="$(rooted_path "$service_file_runtime")"
  update_service_file="$(rooted_path "$update_service_file_runtime")"
  network_recovery_service_file="$(rooted_path "$network_recovery_service_file_runtime")"
  panel_restore_service_file="$(rooted_path "$panel_restore_service_file_runtime")"
  link_path="$(rooted_path "$link_path_runtime")"
}

check_host() {
  if [[ -n "$root_prefix" ]]; then
    return
  fi
  [[ "${EUID}" -eq 0 ]] || die "请使用 root 用户安装或卸载"
  [[ "$(uname -s)" == "Linux" ]] || die "生产安装仅支持 Linux"
  [[ -r /etc/os-release ]] || {
    [[ "$allow_unsupported" == true ]] || die "无法识别 Linux 发行版，可使用 --allow-unsupported"
    return
  }

  # shellcheck disable=SC1091
  source /etc/os-release
  case "${ID:-}" in
    ubuntu|debian|centos|rhel|rocky|almalinux|opencloudos|anolis) ;;
    *)
      [[ "$allow_unsupported" == true ]] ||
        die "尚未验证当前发行版 ${ID:-unknown}，可使用 --allow-unsupported"
      warn "正在未验证的发行版 ${ID:-unknown} 上安装"
      ;;
  esac

  command -v systemctl >/dev/null 2>&1 || die "系统缺少 systemctl"
  command -v setpriv >/dev/null 2>&1 || die "系统缺少 setpriv（util-linux），无法提供低权限 Web 终端"
  command -v prlimit >/dev/null 2>&1 || die "系统缺少 prlimit（util-linux），无法限制 Web 终端资源"
  command -v getent >/dev/null 2>&1 || die "系统缺少 getent，无法校验 Web 终端用户"
}

check_install_sources() {
  [[ -f "$binary_source" ]] || die "找不到二进制文件: ${binary_source}"
  [[ -x "$binary_source" ]] || die "二进制文件不可执行: ${binary_source}"
  [[ -f "$config_source" ]] || die "找不到配置模板: ${config_source}"
  [[ -d "$bundled_scripts_source" ]] || die "找不到内置组件包目录: ${bundled_scripts_source}"
  "$binary_source" version >/dev/null 2>&1 ||
    die "二进制文件无法在当前系统运行，请确认操作系统和 CPU 架构"
}

check_managed_service() {
  if [[ -e "$service_file" ]] && ! grep -Fq "$MANAGED_MARKER" "$service_file"; then
    die "服务文件不属于 OneinStack 安装器，拒绝覆盖: ${service_file_runtime}"
  fi
  if [[ -e "$update_service_file" ]] && ! grep -Fq "$MANAGED_MARKER" "$update_service_file"; then
    die "更新服务文件不属于 OneinStack 安装器，拒绝覆盖: ${update_service_file_runtime}"
  fi
  if [[ -e "$network_recovery_service_file" ]] &&
    ! grep -Fq "$MANAGED_MARKER" "$network_recovery_service_file"; then
    die "网络恢复服务文件不属于 OneinStack 安装器，拒绝覆盖: ${network_recovery_service_file_runtime}"
  fi
  if [[ -e "$panel_restore_service_file" ]] &&
    ! grep -Fq "$MANAGED_MARKER" "$panel_restore_service_file"; then
    die "恢复服务文件不属于 OneinStack 安装器，拒绝覆盖: ${panel_restore_service_file_runtime}"
  fi
}

check_managed_link() {
  if [[ -L "$link_path" ]]; then
    local target
    target="$(readlink "$link_path")"
    [[ "$target" == "${install_dir_runtime}/one" ]] ||
      die "命令链接指向其他程序，拒绝覆盖: ${link_path_runtime} -> ${target}"
  elif [[ -e "$link_path" ]]; then
    die "命令路径已被普通文件占用，拒绝覆盖: ${link_path_runtime}"
  fi
}

backup_existing() {
  [[ -e "${install_dir}/one" || -e "${install_dir}/config.yaml" || -e "$service_file" ]] || return 0

  local backup_dir="${install_dir}/backups/$(date -u '+%Y%m%dT%H%M%SZ')-$$"
  mkdir -p -- "$backup_dir"
  chmod 0700 "$backup_dir"
  [[ -f "${install_dir}/one" ]] && cp -p -- "${install_dir}/one" "${backup_dir}/one"
  [[ -f "${install_dir}/config.yaml" ]] &&
    cp -p -- "${install_dir}/config.yaml" "${backup_dir}/config.yaml"
  [[ -f "$service_file" ]] && cp -p -- "$service_file" "${backup_dir}/one.service"
  [[ -f "$update_service_file" ]] &&
    cp -p -- "$update_service_file" "${backup_dir}/one-update.service"
  [[ -f "$network_recovery_service_file" ]] &&
    cp -p -- "$network_recovery_service_file" "${backup_dir}/one-network-recover.service"
  [[ -f "$panel_restore_service_file" ]] &&
    cp -p -- "$panel_restore_service_file" "${backup_dir}/one-panel-restore.service"
  log "现有安装已备份到 ${backup_dir}"
}

write_service_file() {
  local temporary_service="${service_file}.new.$$"
  local temporary_update_service="${update_service_file}.new.$$"
  local temporary_network_recovery_service="${network_recovery_service_file}.new.$$"
  local temporary_panel_restore_service="${panel_restore_service_file}.new.$$"
  mkdir -p -- "$(dirname "$service_file")"
  cat >"$temporary_service" <<EOF
${MANAGED_MARKER}
[Unit]
Description=OneinStack Panel
Documentation=https://github.com/oneinstack/Oneinstack-Panel
After=network-online.target
Wants=network-online.target
OnFailure=one-network-recover.service
StartLimitIntervalSec=60
StartLimitBurst=3

[Service]
Type=simple
WorkingDirectory=${install_dir_runtime}
Environment=ONEINSTACK_BASE_PATH=${install_dir_runtime}
Environment=ONEINSTACK_CONFIG_PATH=${install_dir_runtime}/config.yaml
ExecStartPre=${install_dir_runtime}/one backup recover --unless-restore-active
ExecStart=${install_dir_runtime}/one server start
Restart=on-failure
RestartSec=5s
TimeoutStopSec=15s
KillSignal=SIGTERM
UMask=0027
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "$temporary_service"
  mv -f -- "$temporary_service" "$service_file"

  cat >"$temporary_update_service" <<EOF
${MANAGED_MARKER}
[Unit]
Description=OneinStack Panel signed update transaction
Documentation=https://github.com/oneinstack/Oneinstack-Panel
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
WorkingDirectory=${install_dir_runtime}
Environment=ONEINSTACK_BASE_PATH=${install_dir_runtime}
Environment=ONEINSTACK_CONFIG_PATH=${install_dir_runtime}/config.yaml
ExecStart=${install_dir_runtime}/one update apply --yes
TimeoutStartSec=30min
UMask=0077
PrivateTmp=true
EOF
  chmod 0644 "$temporary_update_service"
  mv -f -- "$temporary_update_service" "$update_service_file"

  cat >"$temporary_network_recovery_service" <<EOF
${MANAGED_MARKER}
[Unit]
Description=OneinStack Panel network configuration recovery
Documentation=https://github.com/oneinstack/Oneinstack-Panel
After=one.service

[Service]
Type=oneshot
WorkingDirectory=${install_dir_runtime}
Environment=ONEINSTACK_BASE_PATH=${install_dir_runtime}
Environment=ONEINSTACK_CONFIG_PATH=${install_dir_runtime}/config.yaml
ExecStart=${install_dir_runtime}/one network recover
TimeoutStartSec=2min
UMask=0077
PrivateTmp=true
EOF
  chmod 0644 "$temporary_network_recovery_service"
  mv -f -- "$temporary_network_recovery_service" "$network_recovery_service_file"

  cat >"$temporary_panel_restore_service" <<EOF
${MANAGED_MARKER}
[Unit]
Description=OneinStack Panel encrypted backup restore transaction
Documentation=https://github.com/oneinstack/Oneinstack-Panel
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
WorkingDirectory=${install_dir_runtime}
Environment=ONEINSTACK_BASE_PATH=${install_dir_runtime}
Environment=ONEINSTACK_CONFIG_PATH=${install_dir_runtime}/config.yaml
ExecStartPre=/bin/sleep 2
ExecStart=${install_dir_runtime}/one backup restore
TimeoutStartSec=30min
UMask=0077
PrivateTmp=true
EOF
  chmod 0644 "$temporary_panel_restore_service"
  mv -f -- "$temporary_panel_restore_service" "$panel_restore_service_file"
}

install_files() {
  local temporary_binary="${install_dir}/.one.new.$$"
  local temporary_config="${install_dir}/.config.yaml.new.$$"

  mkdir -p -- "$install_dir"
  chmod 0750 "$install_dir"
  mkdir -p -- "$(rooted_path "/data/wwwroot")" \
    "$(rooted_path "/data/wwwlogs")" \
    "$(rooted_path "/data/db")"

  install -m 0755 "$binary_source" "$temporary_binary"
  mv -f -- "$temporary_binary" "${install_dir}/one"

  if [[ ! -e "${install_dir}/config.yaml" || "$replace_config" == true ]]; then
    install -m 0640 "$config_source" "$temporary_config"
    mv -f -- "$temporary_config" "${install_dir}/config.yaml"
  else
    log "保留现有配置 ${install_dir_runtime}/config.yaml"
  fi

  mkdir -p -- "${install_dir}/script-registry/cache" "${install_dir}/script-registry/bundled"
  chmod 0750 "${install_dir}/script-registry" "${install_dir}/script-registry/cache" "${install_dir}/script-registry/bundled"
  cp -R -- "${bundled_scripts_source}/." "${install_dir}/script-registry/bundled/"
  mkdir -p -- "${install_dir}/certificates" "${install_dir}/acme-webroot/.well-known/acme-challenge"
  chmod 0700 "${install_dir}/certificates"
  chmod 0755 "${install_dir}/acme-webroot" \
    "${install_dir}/acme-webroot/.well-known" \
    "${install_dir}/acme-webroot/.well-known/acme-challenge"

  write_service_file
  mkdir -p -- "$(dirname "$link_path")"
  if [[ ! -L "$link_path" ]]; then
    ln -s -- "${install_dir_runtime}/one" "$link_path"
  fi
}

ensure_terminal_user() {
  [[ -z "$root_prefix" ]] || return 0

  local nologin_shell="/usr/sbin/nologin"
  [[ -x "$nologin_shell" ]] || nologin_shell="/sbin/nologin"
  [[ -x "$nologin_shell" ]] || die "系统缺少 nologin，无法创建隔离终端用户"

  if getent passwd "$terminal_user_runtime" >/dev/null 2>&1; then
    local passwd_entry uid home shell groups
    passwd_entry="$(getent passwd "$terminal_user_runtime")"
    IFS=: read -r _ _ uid _ _ home shell <<<"$passwd_entry"
    [[ "$uid" =~ ^[0-9]+$ && "$uid" -ne 0 ]] ||
      die "终端用户 ${terminal_user_runtime} 的 UID 无效"
    [[ "$home" == "$terminal_home_runtime" ]] ||
      die "终端用户 ${terminal_user_runtime} 的主目录不是 ${terminal_home_runtime}"
    [[ "$shell" == "/usr/sbin/nologin" || "$shell" == "/sbin/nologin" ]] ||
      die "终端用户 ${terminal_user_runtime} 必须使用 nologin"
    groups="$(id -nG "$terminal_user_runtime" 2>/dev/null || true)"
    case " ${groups} " in
      *" root "*|*" sudo "*|*" wheel "*|*" admin "*|*" docker "*|*" lxd "*|*" podman "*|*" systemd-journal "*)
        die "终端用户 ${terminal_user_runtime} 不能属于特权用户组"
        ;;
    esac
  else
    command -v useradd >/dev/null 2>&1 ||
      die "系统缺少 useradd，无法创建隔离终端用户"
    useradd \
      --system \
      --user-group \
      --home-dir "$terminal_home_runtime" \
      --create-home \
      --shell "$nologin_shell" \
      --comment "OneinStack isolated web terminal" \
      "$terminal_user_runtime"
  fi

  install -d -m 0750 -o "$terminal_user_runtime" -g "$terminal_user_runtime" \
    "$terminal_home_runtime"
}

password_file_mode_is_safe() {
  local file="$1"
  local mode
  mode="$(stat -c '%a' "$file" 2>/dev/null || true)"
  [[ "$mode" =~ ^[0-7]?[0-7]00$ ]]
}

prompt_for_password() {
  [[ -t 0 && -t 1 ]] ||
    die "非交互安装必须通过 --admin-password-file 提供初始密码"

  local first=""
  local second=""
  read -r -s -p "请输入初始管理员密码: " first
  printf '\n'
  read -r -s -p "请再次输入初始管理员密码: " second
  printf '\n'
  [[ "$first" == "$second" ]] || die "两次输入的密码不一致"
  [[ -n "$first" ]] || die "管理员密码不能为空"

  temporary_password_file="${install_dir}/.admin-password.$$"
  (umask 077 && printf '%s\n' "$first" >"$temporary_password_file")
  first=""
  second=""
  admin_password_file="$temporary_password_file"
}

initialize_admin() {
  [[ "$skip_init" == false ]] || return 0

  if ONEINSTACK_BASE_PATH="$install_dir_runtime" \
    ONEINSTACK_CONFIG_PATH="${install_dir_runtime}/config.yaml" \
    "${install_dir}/one" init --user "$admin_user" >/dev/null 2>&1; then
    log "管理员用户已经存在，跳过初始化"
    return
  fi

  if [[ -z "$admin_password_file" ]]; then
    prompt_for_password
  fi
  [[ -r "$admin_password_file" ]] || die "无法读取管理员密码文件"
  password_file_mode_is_safe "$admin_password_file" ||
    die "管理员密码文件不能允许组或其他用户读取，请使用 chmod 600"

  ONEINSTACK_BASE_PATH="$install_dir_runtime" \
    ONEINSTACK_CONFIG_PATH="${install_dir_runtime}/config.yaml" \
    "${install_dir}/one" init \
    --user "$admin_user" \
    --password-file "$admin_password_file"
}

start_service() {
  [[ -z "$root_prefix" ]] || return 0
  systemctl daemon-reload
  if [[ "$no_enable" == false ]]; then
    systemctl enable one.service
  fi
  if [[ "$no_start" == false ]]; then
    systemctl restart one.service
  fi
}

check_health() {
  [[ "$no_start" == false && "$no_health_check" == false ]] || return 0

  local started_at
  started_at="$(date +%s)"
  while true; do
    if command -v curl >/dev/null 2>&1; then
      if curl --fail --silent --show-error --max-time 3 "$health_url" >/dev/null; then
        log "服务就绪检查通过: ${health_url}"
        return
      fi
    elif command -v wget >/dev/null 2>&1; then
      if wget -q -T 3 -O /dev/null "$health_url"; then
        log "服务就绪检查通过: ${health_url}"
        return
      fi
    else
      die "缺少 curl 或 wget；安装后请自行检查服务，或使用 --no-health-check"
    fi

    if (( $(date +%s) - started_at >= health_timeout )); then
      systemctl status one.service --no-pager || true
      die "服务在 ${health_timeout} 秒内未通过就绪检查: ${health_url}"
    fi
    sleep 1
  done
}

run_install() {
  check_install_sources
  check_managed_service
  check_managed_link

  if [[ -x "${install_dir}/one" && "$force" == false ]]; then
    die "面板已经安装；如需更新，请显式使用 --force"
  fi
  if [[ "$replace_config" == true && "$force" == false && -e "${install_dir}/config.yaml" ]]; then
    die "替换现有配置必须同时使用 --force"
  fi

  if [[ "$force" == true ]]; then
    backup_existing
  fi
  install_files
  ensure_terminal_user
  initialize_admin
  start_service
  check_health

  log "安装完成"
  log "安装目录: ${install_dir_runtime}"
  if [[ -z "$root_prefix" && "$no_start" == false ]]; then
    log "面板地址: ${health_url%/health/ready}"
  fi
}

remove_managed_service_and_link() {
  if [[ -f "$service_file" ]]; then
    grep -Fq "$MANAGED_MARKER" "$service_file" ||
      die "服务文件不属于 OneinStack 安装器，拒绝删除: ${service_file_runtime}"
    rm -f -- "$service_file"
  fi
  if [[ -f "$update_service_file" ]]; then
    grep -Fq "$MANAGED_MARKER" "$update_service_file" ||
      die "更新服务文件不属于 OneinStack 安装器，拒绝删除: ${update_service_file_runtime}"
    rm -f -- "$update_service_file"
  fi
  if [[ -f "$network_recovery_service_file" ]]; then
    grep -Fq "$MANAGED_MARKER" "$network_recovery_service_file" ||
      die "网络恢复服务文件不属于 OneinStack 安装器，拒绝删除: ${network_recovery_service_file_runtime}"
    rm -f -- "$network_recovery_service_file"
  fi
  if [[ -f "$panel_restore_service_file" ]]; then
    grep -Fq "$MANAGED_MARKER" "$panel_restore_service_file" ||
      die "恢复服务文件不属于 OneinStack 安装器，拒绝删除: ${panel_restore_service_file_runtime}"
    rm -f -- "$panel_restore_service_file"
  fi

  if [[ -L "$link_path" ]]; then
    local target
    target="$(readlink "$link_path")"
    [[ "$target" == "${install_dir_runtime}/one" ]] ||
      die "命令链接指向其他程序，拒绝删除: ${link_path_runtime}"
    rm -f -- "$link_path"
  fi
}

run_uninstall() {
  if [[ "$purge" == true && "$assume_yes" == false ]]; then
    die "--purge 会永久删除配置和数据库，必须同时使用 --yes"
  fi

  if [[ -z "$root_prefix" ]] && command -v systemctl >/dev/null 2>&1; then
    systemctl stop one-network-recover.service >/dev/null 2>&1 || true
    systemctl stop one-update.service >/dev/null 2>&1 || true
    systemctl stop one-panel-restore.service >/dev/null 2>&1 || true
    systemctl disable --now one.service >/dev/null 2>&1 || true
  fi
  remove_managed_service_and_link

  if [[ "$purge" == true ]]; then
    validate_install_dir
    [[ "$install_dir" == "$(rooted_path "$install_dir_runtime")" ]] ||
      die "卸载目录校验失败"
    if [[ -d "$install_dir" ]]; then
      rm -rf -- "$install_dir"
    fi
    log "面板程序、配置、数据库、日志和备份已永久删除"
  else
    rm -f -- "${install_dir}/one"
    log "面板程序已卸载，配置和数据保留在 ${install_dir_runtime}"
  fi

  if [[ -z "$root_prefix" ]] && command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload
  fi
}

main() {
  parse_arguments "$@"
  prepare_paths
  check_host

  case "$action" in
    install) run_install ;;
    uninstall) run_uninstall ;;
    *) die "不支持的操作: ${action}" ;;
  esac
}

main "$@"
