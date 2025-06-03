#!/bin/bash

# 默认值
ROOT_PASSWORD=""
MYSQL_PORT=3306
VERSION=""
# 函数：显示帮助信息
usage() {
  echo "Usage: $0 -p <root_password> -P <mysql_port> -v <version>"
  echo "  -p  设置 MySQL root 密码 (必需)"
  echo "  -P  设置 MySQL 端口号 (默认: 3306)"
  echo "  -v  设置 MySQL 版本"

}

# 解析命令行参数
while getopts "p:P:hv:" opt; do
  case "$opt" in
    p) ROOT_PASSWORD="$OPTARG" ;;  # 设置 root 密码
    P) MYSQL_PORT="$OPTARG" ;;
    v) VERSION="$OPTARG" ;;        # 必填版本参数
    *) usage ;;  # 不支持的选项
  esac
done

# 检查是否传入了 root 密码
if [ -z "$ROOT_PASSWORD" ]; then
  echo "请通过 -p 参数传入 MySQL root 密码，例如：$0 -p <root_password>"
  exit 1
fi

# 检查必填参数 -v 是否存在
if [ -z "$VERSION" ]; then
  echo "错误：必须使用 -v 指定版本号"
  usage
  exit 1
fi

# 确保脚本以 root 用户执行
if [ "$(id -u)" -ne 0 ]; then
  echo "请使用 root 用户执行该脚本"
  exit 1
fi

# 定义函数来检测和选择包管理器
setup_package_manager() {
  if command -v apt-get > /dev/null 2>&1; then
    PACKAGE_MANAGER="apt-get"
  elif command -v yum > /dev/null 2>&1; then
    PACKAGE_MANAGER="yum"
  elif command -v dnf > /dev/null 2>&1; then
    PACKAGE_MANAGER="dnf"
  else
    echo "不支持的包管理器"
    exit 1
  fi
}

# 更新系统包
update_packages() {
  echo "更新系统包..."
  $PACKAGE_MANAGER update -y
}

# 安装依赖项
install_dependencies() {
  echo "安装 MySQL 所需的依赖..."
  case $PACKAGE_MANAGER in
    apt-get)
      $PACKAGE_MANAGER install -y build-essential cmake libncurses5-dev libssl-dev libboost-all-dev bison
      ;;
    yum|dnf)
      $PACKAGE_MANAGER install -y gcc gcc-c++ make cmake ncurses-devel openssl-devel boost-devel bison
      ;;
  esac
}

# 下载并解压 MySQL 源码包
download_and_extract_mysql() {
  declare -A VERSION_MAP=(
      ["5.5"]="5.5.62"
      ["5.6"]="5.6.51"
      ["5.7"]="5.7.40"
      ["8.0"]="8.0.36"
      ["8.2"]="8.2.0"
  )

  # 检查传入的主版本是否受支持
  if [[ -z "${VERSION_MAP[$VERSION]}" ]]; then
    echo "错误：不支持的主版本 '$VERSION'，可选版本：${!VERSION_MAP[@]}"
    exit 1
  fi

  local FULL_VERSION="${VERSION_MAP[$VERSION]}"
  local MYSQL_VERSION="mysql-${FULL_VERSION}"
  local MYSQL_TAR="$MYSQL_VERSION.tar.gz"

  echo "下载 MySQL 源码包..."
  wget https://dev.mysql.com/get/Downloads/MySQL-${VERSION}/$MYSQL_TAR

  echo "解压 MySQL 源码包..."
  tar -xvzf $MYSQL_TAR
  cd $MYSQL_VERSION
}

# 编译并安装 MySQL
compile_and_install_mysql() {
  echo "创建 MySQL 安装目录..."
  sudo mkdir -p /usr/local/mysql

  echo "编译 MySQL..."
  cmake . -DCMAKE_INSTALL_PREFIX=/usr/local/mysql \
          -DMYSQL_DATADIR=/usr/local/mysql/data \
          -DDEFAULT_CHARSET=utf8 \
          -DDEFAULT_COLLATION=utf8_general_ci \
          -DWITH_INNOBASE_STORAGE_ENGINE=1 \
          -DWITH_PARTITION_STORAGE_ENGINE=1 \
          -DWITH_FEDERATED_STORAGE_ENGINE=1 \
          -DWITH_BLACKHOLE_STORAGE_ENGINE=1 \
          -DWITH_MYISAM_STORAGE_ENGINE=1 \
          -DWITH_ARCHIVE_STORAGE_ENGINE=1 \
          -DEXTRA_CHARSETS=all \
          -DENABLED_LOCAL_INFILE=1

  echo "安装 MySQL..."
  make -j$(nproc)
  sudo make install
}

# 创建 MySQL 用户并设置权限
create_mysql_user_and_set_permissions() {
  echo "创建 MySQL 用户..."
  sudo useradd -r -s /bin/false mysql

  echo "设置 MySQL 目录权限..."
  sudo chown -R mysql:mysql /usr/local/mysql
  sudo chown -R mysql:mysql /usr/local/mysql/data
}

# 初始化数据库
initialize_database() {
  echo "初始化 MySQL 数据库..."
  sudo /usr/local/mysql/scripts/mysql_install_db --user=mysql --basedir=/usr/local/mysql --datadir=/usr/local/mysql/data
}

# 配置 MySQL 服务
configure_mysql_service() {
  echo "配置 MySQL 服务..."
  if command -v systemctl > /dev/null 2>&1; then
    sudo cp /usr/local/mysql/support-files/mysql.server /etc/init.d/mysql
    sudo chmod +x /etc/init.d/mysql
    sudo systemctl daemon-reload
    sudo systemctl enable mysql
  else
    sudo chkconfig --add mysql
    sudo chkconfig mysql on
  fi
}

# 设置环境变量
set_env_vars() {
  echo 'export PATH=$PATH:/usr/local/mysql/bin' >> ~/.bashrc
  source ~/.bashrc
}

# 启动 MySQL 服务并设置 root 密码
start_mysql_and_set_root_password() {
  echo "启动 MySQL 服务..."
  sudo service mysql start || sudo /etc/init.d/mysql start

  echo "设置 MySQL root 密码..."
  sudo /usr/local/mysql/bin/mysqladmin -u root password "$ROOT_PASSWORD"
}

# 主函数
main() {
  setup_package_manager
  update_packages
  install_dependencies
  download_and_extract_mysql
  compile_and_install_mysql
  create_mysql_user_and_set_permissions
  initialize_database
  configure_mysql_service
  set_env_vars
  start_mysql_and_set_root_password

  echo "MySQL 5.5 安装完成，root 密码已设置为：$ROOT_PASSWORD"
}

main