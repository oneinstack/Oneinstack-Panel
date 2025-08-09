package software

import (
	"errors"
	"fmt"
	"log"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/router/input"
	"oneinstack/utils"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"gorm.io/gorm"
)

type InstallOPI interface {
	Install() (string, error)
}

type InstallOP struct {
	BashParams *input.InstallParams
	Remote     bool
}

func NewInstallOP(p *input.InstallParams) (InstallOP, error) {
	s := &models.Software{}
	tx := app.DB().Where("key = ? and version = ?", p.Key, p.Version).First(s)
	if tx.Error != nil && errors.Is(tx.Error, gorm.ErrRecordNotFound) {
		return InstallOP{}, tx.Error
	}
	return InstallOP{BashParams: p, Remote: s.Resource != "local"}, nil
}

func (ps InstallOP) Install(sync ...bool) (string, error) {
	defer ps.updateSoft()
	sy := false
	if len(sync) > 0 {
		sy = sync[0]
	}

	// 使用新的安装器
	installer := NewInstaller()
	return installer.Install(ps.BashParams, !sy) // !sy 因为sync=true表示同步，async=false
}

func (ps InstallOP) updateSoft() error {
	s := &models.Software{}
	tx := app.DB().Where("key = ? and version = ?", ps.BashParams.Key, ps.BashParams.Version).First(s)
	if tx.Error != nil {
		return tx.Error
	}
	s.InstallTime = time.Now()
	switch s.Key {
	case "webserver":
		s.HttpPort = "80"
		s.HttpPort = "443"
	case "db":
		s.HttpPort = ps.BashParams.Port
		s.RootPwd = ps.BashParams.Pwd
	case "redis":
		s.HttpPort = ps.BashParams.Port
		s.RootPwd = ps.BashParams.Pwd
	case "php":
	case "java":
	case "phpmyadmin":
		s.HttpPort = "8080"
		s.UrlPath = "http://$IP_ADDRESS:8080/phpmyadmin"
	default:
		return fmt.Errorf("未知的类型")
	}
	return nil
}

func (ps InstallOP) executeShScriptRemote(fn string, sy bool, args ...string) (string, error) {
	return ps.executeShScript(fn, sy, args...)
}

func (ps InstallOP) executeShScriptLocal(fn string, sy bool, parms input.InstallParams) (string, error) {
	switch ps.BashParams.Key {
	case "webserver":
		return ps.executeShScript(fn, sy)
	case "db":
		return ps.executeShScript(fn, sy, parms.Username, parms.Pwd)
	case "redis":
		return ps.executeShScript(fn, sy, "7")
	case "php":
		if ps.BashParams.Version == "5.6" {
			return ps.executeShScript(fn, sy, "5")
		}
		if ps.BashParams.Version == "7.4" {
			return ps.executeShScript(fn, sy, "7")
		}
		if ps.BashParams.Version == "8.1" {
			return ps.executeShScript(fn, sy, "8")
		}
		return "", nil
	case "java":
		return ps.executeShScript(fn, sy, "-v", ps.BashParams.Version)
	case "phpmyadmin":
		return ps.executeShScript(fn, sy)
	case "openresty":
		return ps.executeShScript(fn, sy)
	default:
		return "", fmt.Errorf("未知的软件类型")
	}
}

func (ps InstallOP) getScript() (string, error) {
	if ps.Remote {
		return ps.getScriptRemote()
	} else {
		return ps.getScriptLocal()
	}
}

func (ps InstallOP) getScriptRemote() (string, error) {
	s := &models.Software{}
	tx := app.DB().Where("key = ? and version = ?", ps.BashParams.Key, ps.BashParams.Version).First(s)
	if tx.Error != nil {
		return "", tx.Error
	}
	return s.Script, nil
}

func (ps InstallOP) getScriptLocal() (string, error) {
	bash := ""
	switch ps.BashParams.Key {
	case "webserver":
		bash = nginx
	case "phpmyadmin":
		bash = phpmyadmin
	case "db":
		if ps.BashParams.Version == "5.5" {
			bash = mysql55
		}
		if ps.BashParams.Version == "5.7" {
			bash = mysql57
		}
		if ps.BashParams.Version == "8.0" {
			bash = mysql80
		}
	case "redis":
		bash = redis
	case "php":
		/*data, err := InstallPhp.ReadFile("scripts/InstallPhp.sh")
		if err != nil {
			return "", err
		}
		bash = string(data)*/
		if ps.BashParams.Version == "7.4" {
			bash = php
		}
		if ps.BashParams.Version == "8.4" {
			bash = php
		}
		if ps.BashParams.Version == "5.6" {
			bash = php
		}
	case "java":
		if ps.BashParams.Version == "11" {
			bash = openJDK11
			/*data, err := InstallJava11.ReadFile("scripts/InstallJava11.sh")
			if err != nil {
				return "", err
			}
			bash = string(data)*/
		}
		if ps.BashParams.Version == "17" {
			bash = openJDK17
		}
		if ps.BashParams.Version == "18" {
			bash = openJDK18
		}
	case "openresty":
		bash = openresty
	default:
		return "", fmt.Errorf("未知的软件类型")
	}
	return bash, nil
}

// createShScript 将字符串内容保存为.sh脚本文件，如果文件已存在则覆盖
func (ps InstallOP) createShScript(scriptContent, filename string) (string, error) {
	// 打开文件，如果文件不存在则创建，权限设置为可读可写可执行
	file, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return "", fmt.Errorf("无法打开文件: %v", err)
	}
	defer file.Close()

	// 写入脚本内容
	_, err = file.WriteString(scriptContent)
	if err != nil {
		return "", fmt.Errorf("写入文件失败: %v", err)
	}

	// 打印成功信息
	fmt.Printf("脚本已保存为 %s\n", filename)
	return filename, nil
}

// executeShScript 执行指定的脚本文件，并支持传递命令行参数
func (ps InstallOP) executeShScript(scriptName string, sync bool, args ...string) (string, error) {

	// 拼接完整的命令：bash scriptName args...
	cmdArgs := append([]string{scriptName}, args...)
	cmd := exec.Command("bash", cmdArgs...)

	logFileName := "install_" + time.Now().Format("2006-01-02_15-04-05") + ".log"
	// 判断路径是否存在
	if _, err := os.Stat("/data/wwwlogs/install/"); os.IsNotExist(err) {
		os.MkdirAll("/data/wwwlogs/install/", 0777)
	}
	// 创建日志文件
	logFile, err := os.Create("/data/wwwlogs/install/" + logFileName)
	if err != nil {
		return "", fmt.Errorf("无法创建日志文件: %v", err)
	}
	defer logFile.Close()

	cmd.Stdout = logFile
	cmd.Stderr = logFile
	err = cmd.Start()
	if err != nil {
		return "", err
	}
	app.DB().Where("key = ? and version = ?", ps.BashParams.Key, ps.BashParams.Version).
		Updates(&models.Software{Status: models.Soft_Status_Ing, Log: logFileName})

	done := make(chan error, 1)

	go func(bp *input.InstallParams) {
		defer func() {
			if e := recover(); e != nil {
				log.Println("InstallParams panic error:", e)
			}
		}()

		fmt.Println("cmd running: " + scriptName)
		errs := cmd.Wait() // 一定要用局部的 err，不要用外面的 err
		fmt.Println("cmd done: " + scriptName)

		done <- errs
	}(ps.BashParams)

	// 此处非阻塞执行后续逻辑
	go func() {
		errs := <-done // 此处会阻塞，等待脚本完成

		var status int
		var installed bool
		var installVersion string

		if errs != nil {
			fmt.Println("脚本执行失败: ", errs)
			status = models.Soft_Status_Default
			installed = false
			installVersion = ""
		} else {
			fmt.Println("脚本执行成功")
			status = models.Soft_Status_Suc
			installed = true
			installVersion = ps.BashParams.Version
		}
		um := map[string]interface{}{
			"status":          status,
			"installed":       installed,
			"install_version": installVersion,
			"log":             logFileName,
		}

		app.DB().Model(&models.Software{}).Where("key = ? and version = ?", ps.BashParams.Key, ps.BashParams.Version).
			Updates(um)
	}()
	return logFileName, nil
}

func (ps InstallOP) buildRemoteArgs() ([]string, error) {

	return nil, nil
}

// checkIfFileExists 检查文件是否存在。
func checkIfFileExists(filename string) bool {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return false
	}
	return true
}

// downloadOneInstack 如果 oneinstack.tar.gz 不存在则下载它。
func downloadshell() error {
	tarFilePath := filepath.Join(".", "oneinstack.tar.gz")
	if !checkIfFileExists(tarFilePath) {
		fmt.Println("oneinstack.tar.gz does not exist. Downloading...")
		err := utils.DownloadFile("https://mirrors.oneinstack.com/oneinstack.tar.gz", tarFilePath)
		if err != nil {
			return err
		}
		fmt.Printf("Download completed.\n")
	} else {
		fmt.Println("oneinstack.tar.gz already exists, skipping download.")
	}
	return utils.DecompressTarGz(tarFilePath, filepath.Join(".", "oneinstack"))
}

var mysql55 = `
#!/bin/bash

# 默认值
ROOT_PASSWORD=""
MYSQL_PORT=3306
# 函数：显示帮助信息
usage() {
  echo "Usage: $0 -p <root_password> -P <mysql_port>"
  echo "  -p  设置 MySQL root 密码 (必需)"
  echo "  -P  设置 MySQL 端口号 (默认: 3306)"
}

# 解析命令行参数
while getopts "p:P:h" opt; do
  case "$opt" in
    p) ROOT_PASSWORD="$OPTARG" ;;  # 设置 root 密码
    P) MYSQL_PORT="$OPTARG" ;;
    *) usage ;;  # 不支持的选项
  esac
done

# 检查是否传入了 root 密码
if [ -z "$ROOT_PASSWORD" ]; then
  echo "请通过 -p 参数传入 MySQL root 密码，例如：$0 -p <root_password>"
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
  local MYSQL_VERSION="mysql-5.5.62"
  local MYSQL_TAR="$MYSQL_VERSION.tar.gz"
  
  echo "下载 MySQL 5.5 源码包..."
  wget https://dev.mysql.com/get/Downloads/MySQL-5.5/$MYSQL_TAR
  
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

`

var mysql57 = `
#!/bin/bash

# 默认参数
MYSQL_VERSION="5.7.40"
MYSQL_DOWNLOAD_URL="https://dev.mysql.com/get/Downloads/MySQL-5.7/mysql-${MYSQL_VERSION}.tar.gz"
MYSQL_INSTALL_DIR="/usr/local/mysql"
MYSQL_DATA_DIR="/data/mysql"
MYSQL_ROOT_PASSWORD=""
MYSQL_PORT=3306

# 帮助信息
usage() {
  echo "Usage: $0 -p <root_password> -P <mysql_port>"
  echo "  -p  设置 MySQL root 密码 (必需)"
  echo "  -P  设置 MySQL 端口号 (默认: 3306)"
  exit 1
}

# 解析参数
while getopts "p:P:h" opt; do
  case $opt in
    p) MYSQL_ROOT_PASSWORD="$OPTARG" ;;
    P) MYSQL_PORT="$OPTARG" ;;
    h) usage ;;
    *) usage ;;
  esac
done

# 检查是否提供了 root 密码
if [ -z "$MYSQL_ROOT_PASSWORD" ]; then
  echo "错误: 必须提供 root 密码 (-p)"
  usage
fi

# 检查是否为 root 用户
if [ "$(id -u)" != "0" ]; then
  echo "请以 root 用户运行该脚本"
  exit 1
fi

# 检测系统类型
Detect_OS() {
  if [ -f /etc/redhat-release ]; then
    OS="CentOS"
    PM="yum"
  elif [ -f /etc/debian_version ]; then
    OS="Debian"
    PM="apt"
  else
    echo "不支持的操作系统"
    exit 1
  fi
}

# 安装依赖
Install_Dependencies() {
  echo "安装必要依赖包..."
  if [ "$PM" == "yum" ]; then
    yum -y install gcc gcc-c++ cmake ncurses-devel bison wget perl make libaio-devel
  elif [ "$PM" == "apt" ]; then
    apt update
    apt -y install build-essential cmake libncurses5-dev libaio-dev bison wget
  fi
}

# 创建 mysql 用户和组
Create_MySQL_User() {
  if ! id -u mysql &>/dev/null; then
    echo "创建 mysql 用户和组..."
    groupadd mysql
    useradd -r -g mysql -s /bin/false mysql
  else
    echo "mysql 用户和组已存在"
  fi
}

# 下载 MySQL 源码
Download_MySQL() {
  if [ ! -f "mysql-${MYSQL_VERSION}.tar.gz" ]; then
    echo "下载 MySQL ${MYSQL_VERSION}..."
    wget -c "${MYSQL_DOWNLOAD_URL}" || { echo "下载 MySQL 失败"; exit 1; }
  fi
  tar xf "mysql-${MYSQL_VERSION}.tar.gz"
}

# 编译并安装 MySQL
Install_MySQL() {
  cd "mysql-${MYSQL_VERSION}" || exit
  cmake . \
  -DCMAKE_INSTALL_PREFIX=${MYSQL_INSTALL_DIR} \
  -DMYSQL_DATADIR=${MYSQL_DATA_DIR} \
  -DWITH_INNOBASE_STORAGE_ENGINE=1 \
  -DWITH_ARCHIVE_STORAGE_ENGINE=1 \
  -DWITH_BLACKHOLE_STORAGE_ENGINE=1 \
  -DWITH_FEDERATED_STORAGE_ENGINE=1 \
  -DWITH_PARTITION_STORAGE_ENGINE=1 \
  -DENABLED_LOCAL_INFILE=1 \
  -DWITH_SSL=bundled \
  -DWITH_ZLIB=bundled \
  -DWITH_BOOST=boost \
  -DCMAKE_C_FLAGS="-fPIC" \
  -DDEFAULT_CHARSET=utf8 \
  -DDEFAULT_COLLATION=utf8_general_ci \
  -DMYSQL_TCP_PORT=${MYSQL_PORT} \
  -DMYSQL_UNIX_ADDR=/tmp/mysql.sock

  make -j"$(nproc)"
  make install
  cd ..
}

# 初始化 MySQL
Initialize_MySQL() {
  echo "初始化 MySQL 数据目录..."

  # 检查并创建数据目录
  if [ ! -d "${MYSQL_DATA_DIR}" ]; then
    echo "创建数据目录：${MYSQL_DATA_DIR}..."
    mkdir -p "${MYSQL_DATA_DIR}"
    chown -R mysql:mysql "${MYSQL_DATA_DIR}"
    chmod 750 "${MYSQL_DATA_DIR}"
  fi

  # 初始化数据目录
  ${MYSQL_INSTALL_DIR}/bin/mysqld --initialize-insecure --user=mysql --basedir=${MYSQL_INSTALL_DIR} --datadir=${MYSQL_DATA_DIR}

  echo "启动 MySQL 服务..."
  ${MYSQL_INSTALL_DIR}/bin/mysqld_safe --user=mysql --port=${MYSQL_PORT} &
  sleep 10

  echo "修改 root 密码..."
  ${MYSQL_INSTALL_DIR}/bin/mysqladmin -uroot password "${MYSQL_ROOT_PASSWORD}" || echo "无法修改密码，请手动检查。"

  echo "MySQL 初始化完成，root 密码已设置为: ${MYSQL_ROOT_PASSWORD}, 端口: ${MYSQL_PORT}"
}

# 配置环境变量
Configure_Environment() {
  echo "配置环境变量..."
  if ! grep -q "${MYSQL_INSTALL_DIR}/bin" /etc/profile; then
    echo "export PATH=\$PATH:${MYSQL_INSTALL_DIR}/bin" >> /etc/profile
    source /etc/profile
  fi
  echo "环境变量配置完成"
}

# 启动 MySQL
Start_MySQL() {
  echo "启动 MySQL 服务..."
  ${MYSQL_INSTALL_DIR}/bin/mysqld_safe --user=mysql --port=${MYSQL_PORT} &
  echo "MySQL 启动完成，端口: ${MYSQL_PORT}"
}

# 主函数
Main() {
  Detect_OS
  Install_Dependencies
  Create_MySQL_User
  Download_MySQL
  Install_MySQL
  Initialize_MySQL
  Configure_Environment
  Start_MySQL
  echo "MySQL ${MYSQL_VERSION} 安装完成，root 密码: ${MYSQL_ROOT_PASSWORD}, 端口: ${MYSQL_PORT}"
}

# 执行主函数
Main

`

var mysql80 = `
#!/bin/bash
# MySQL 8.2 standalone installation script

# Variables (you should customize these as needed)
mysql82_ver="8.2.0"
boost_ver="1.82.0"
mysql_install_dir="/usr/local/mysql"
mysql_data_dir="/data/mysql"
dbinstallmethod=%s  # 1: binary installation, 2: compile from source
dbrootpwd=%s
Mem=$(free -m | awk '/Mem:/{print $2}')

# Ensure mysql user exists
id -u mysql >/dev/null 2>&1 || useradd -M -s /sbin/nologin mysql

mkdir -p ${mysql_install_dir} ${mysql_data_dir}
chown mysql:mysql -R ${mysql_data_dir}

# Download and install MySQL
cd /usr/local/one/src

if [ "${dbinstallmethod}" == "1" ]; then
  wget https://mirrors.oneinstack.com/oneinstack/src/mysql-${mysql82_ver}-linux-glibc2.17-x86_64.tar.xz
  tar xJf mysql-${mysql82_ver}-linux-glibc2.17-x86_64.tar.xz
  mv mysql-${mysql82_ver}-linux-glibc2.17-x86_64/* ${mysql_install_dir}
else
  boostVersion2=$(echo ${boost_ver} | awk -F. '{print $1"_"$2"_"$3}')
  wget https://mirrors.oneinstack.com/oneinstack/src/boost_${boostVersion2}.tar.gz
  wget https://mirrors.oneinstack.com/oneinstack/src/mysql-${mysql82_ver}.tar.gz
  tar xzf boost_${boostVersion2}.tar.gz
  tar xzf mysql-${mysql82_ver}.tar.gz
  cd mysql-${mysql82_ver}
  cmake . -DCMAKE_INSTALL_PREFIX=${mysql_install_dir} \
    -DMYSQL_DATADIR=${mysql_data_dir} \
    -DDOWNLOAD_BOOST=1 \
    -DWITH_BOOST=../boost_${boostVersion2} \
    -DSYSCONFDIR=/etc \
    -DWITH_INNOBASE_STORAGE_ENGINE=1 \
    -DWITH_MYISAM_STORAGE_ENGINE=1 \
    -DDEFAULT_CHARSET=utf8mb4
  make -j $(nproc)
  make install
fi

# MySQL configuration
cat > /etc/my.cnf << EOF
[client]
port = 3306
socket = /tmp/mysql.sock
default-character-set = utf8mb4

[mysqld]
user = mysql
basedir = ${mysql_install_dir}
datadir = ${mysql_data_dir}
port = 3306
socket = /tmp/mysql.sock
default_authentication_plugin = mysql_native_password
character-set-server = utf8mb4
collation-server = utf8mb4_0900_ai_ci
bind-address = 0.0.0.0

log_error = ${mysql_data_dir}/mysql-error.log
pid-file = ${mysql_data_dir}/mysql.pid

max_connections = $((Mem / 3))
innodb_buffer_pool_size = $((Mem / 2))M

EOF

${mysql_install_dir}/bin/mysqld --initialize-insecure --user=mysql --basedir=${mysql_install_dir} --datadir=${mysql_data_dir}

cp ${mysql_install_dir}/support-files/mysql.server /etc/init.d/mysqld
sed -i "s@^basedir=.*@basedir=${mysql_install_dir}@" /etc/init.d/mysqld
sed -i "s@^datadir=.*@datadir=${mysql_data_dir}@" /etc/init.d/mysqld
chmod +x /etc/init.d/mysqld

chkconfig --add mysqld
chkconfig mysqld on

service mysqld start

# Set root password
${mysql_install_dir}/bin/mysql -uroot -e "ALTER USER 'root'@'localhost' IDENTIFIED BY '${dbrootpwd}';"
${mysql_install_dir}/bin/mysql -uroot -p${dbrootpwd} -e "RESET MASTER;"

# Update system PATH
echo "export PATH=${mysql_install_dir}/bin:\$PATH" >> /etc/profile
source /etc/profile

# Final message
echo "MySQL 8.2 installation completed successfully!"


`

var redis = `
#!/bin/bash
# Redis standalone installation script

# Customizable variables
redis_ver="7.2.3"
redis_install_dir="/usr/local/redis"
THREAD=$(nproc)
Mem=$(free -m | awk '/Mem:/{print $2}')

# 创建 redis 用户和组
if ! id -u redis &>/dev/null; then
  echo "创建 redis 用户和组..."
  groupadd redis
  useradd -r -g redis -s /sbin/nologin redis
fi

# Download Redis
cd /usr/local/src
wget https://download.redis.io/releases/redis-${redis_ver}.tar.gz

tar xzf redis-${redis_ver}.tar.gz
cd redis-${redis_ver}

# Compile Redis
make -j ${THREAD}

if [ -f "src/redis-server" ]; then
  mkdir -p ${redis_install_dir}/{bin,etc,var}
  cp src/{redis-benchmark,redis-check-aof,redis-check-rdb,redis-cli,redis-sentinel,redis-server} ${redis_install_dir}/bin/
  cp redis.conf ${redis_install_dir}/etc/
  ln -sf ${redis_install_dir}/bin/* /usr/local/bin/

  sed -i 's@pidfile.*@pidfile /var/run/redis/redis.pid@' ${redis_install_dir}/etc/redis.conf
  sed -i "s@logfile.*@logfile ${redis_install_dir}/var/redis.log@" ${redis_install_dir}/etc/redis.conf
  sed -i "s@^dir.*@dir ${redis_install_dir}/var@" ${redis_install_dir}/etc/redis.conf
  sed -i 's@daemonize no@daemonize yes@' ${redis_install_dir}/etc/redis.conf
  sed -i "s@^# bind 127.0.0.1@bind 127.0.0.1@" ${redis_install_dir}/etc/redis.conf

  redis_maxmemory=$(($Mem / 8))000000
  sed -i "/^maxmemory /d" ${redis_install_dir}/etc/redis.conf
  echo "maxmemory ${redis_maxmemory}" >> ${redis_install_dir}/etc/redis.conf

  # Create redis user if not exists
  id -u redis >/dev/null 2>&1 || useradd -M -s /sbin/nologin redis
  chown -R redis:redis ${redis_install_dir}/{var,etc}

  # Setup systemd service
  cat > /lib/systemd/system/redis-server.service <<EOF
[Unit]
Description=Redis In-Memory Data Store
After=network.target

[Service]
User=redis
Group=redis
ExecStart=${redis_install_dir}/bin/redis-server ${redis_install_dir}/etc/redis.conf
ExecStop=${redis_install_dir}/bin/redis-cli shutdown
Restart=always

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable redis-server
  systemctl start redis-server

  echo "Redis ${redis_ver} installation completed successfully!"
else
  echo "Redis-server install failed. Please check the logs."
  exit 1
fi

`

var nginx = `
#!/bin/bash

#=============================================================================
# Nginx 安装脚本 - 优化版本
# 版本: 2.0
# 描述: 自动检测系统环境，编译安装最新稳定版Nginx
#=============================================================================

set -euo pipefail  # 严格模式：遇到错误立即退出

# 颜色定义
readonly RED='\033[0;31m'
readonly GREEN='\033[0;32m'
readonly YELLOW='\033[1;33m'
readonly NC='\033[0m'

# 配置变量
readonly NGINX_VERSION="1.26.2"        # 最新稳定版
readonly PCRE_VERSION="8.45"
readonly OPENSSL_VERSION="3.0.12"      # 更新的OpenSSL版本
readonly ZLIB_VERSION="1.3.1"

# 目录配置
readonly ONEINSTACK_DIR="/usr/local/one"
readonly NGINX_INSTALL_DIR="/usr/local/nginx"
readonly WWW_ROOT_DIR="/data/wwwroot"
readonly WWW_LOGS_DIR="/data/wwwlogs"
readonly SRC_DIR="${ONEINSTACK_DIR}/src"

# 用户配置
readonly RUN_USER="www"
readonly RUN_GROUP="www"

# 系统配置
readonly THREAD=$(nproc)

# 日志函数
log() {
    local level=$1
    shift
    local message="$*"
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    
    case $level in
        "INFO")  echo -e "${GREEN}[INFO]${NC} ${timestamp} - $message" ;;
        "WARN")  echo -e "${YELLOW}[WARN]${NC} ${timestamp} - $message" ;;
        "ERROR") echo -e "${RED}[ERROR]${NC} ${timestamp} - $message" ;;
    esac
}

# 错误处理函数
error_exit() {
    log "ERROR" "$1"
    exit 1
}

# 检查root权限
check_root() {
    if [[ $EUID -ne 0 ]]; then
        error_exit "请使用root权限运行此脚本"
    fi
}

# 检测操作系统
detect_os() {
    if [[ -f /etc/os-release ]]; then
        . /etc/os-release
        OS=$ID
        log "INFO" "检测到操作系统: $PRETTY_NAME"
    else
        error_exit "无法检测操作系统类型"
    fi
}

# 创建必要目录
create_directories() {
    log "INFO" "创建必要目录..."
    
    local dirs=(
        "$ONEINSTACK_DIR"
        "$SRC_DIR" 
        "$NGINX_INSTALL_DIR"
        "$WWW_ROOT_DIR"
        "$WWW_LOGS_DIR"
        "$WWW_ROOT_DIR/default"
        "$NGINX_INSTALL_DIR/conf/vhost"
    )
    
    for dir in "${dirs[@]}"; do
        mkdir -p "$dir" || error_exit "无法创建目录: $dir"
    done
}

# 创建用户和组
create_user_group() {
    log "INFO" "创建用户和组..."
    
    if ! getent group "$RUN_GROUP" >/dev/null 2>&1; then
        groupadd "$RUN_GROUP" || error_exit "无法创建组: $RUN_GROUP"
    fi
    
    if ! getent passwd "$RUN_USER" >/dev/null 2>&1; then
        useradd -g "$RUN_GROUP" -M -s /sbin/nologin "$RUN_USER" || error_exit "无法创建用户: $RUN_USER"
    fi
    
    if ! getent passwd "nginx" >/dev/null 2>&1; then
        useradd -r -s /sbin/nologin nginx || error_exit "无法创建nginx用户"
    fi
}

# 安装依赖包
install_dependencies() {
    log "INFO" "安装编译依赖..."
    
    case $OS in
        ubuntu|debian)
            export DEBIAN_FRONTEND=noninteractive
            apt-get update || error_exit "更新软件包列表失败"
            apt-get install -y \
                build-essential \
                libpcre3-dev \
                libssl-dev \
                zlib1g-dev \
                wget \
                curl \
                unzip \
                ca-certificates \
                || error_exit "安装依赖包失败"
            ;;
        centos|rhel|rocky|almalinux)
            yum groupinstall -y "Development Tools" || error_exit "安装开发工具失败"
            yum install -y \
                pcre-devel \
                openssl-devel \
                zlib-devel \
                wget \
                curl \
                unzip \
                ca-certificates \
                || error_exit "安装依赖包失败"
            ;;
        fedora)
            dnf groupinstall -y "Development Tools" || error_exit "安装开发工具失败"
            dnf install -y \
                pcre-devel \
                openssl-devel \
                zlib-devel \
                wget \
                curl \
                unzip \
                ca-certificates \
                || error_exit "安装依赖包失败"
            ;;
        *)
            error_exit "不支持的操作系统: $OS"
            ;;
    esac
}

# 下载源码
download_sources() {
    log "INFO" "下载源码包..."
    
    cd "$SRC_DIR" || error_exit "无法进入源码目录"
    
    # 下载函数
    download_with_retry() {
        local filename=$1
        local url1=$2
        local url2=$3
        
        log "INFO" "下载 $filename"
        if ! wget -t 3 -T 30 -O "$filename" "$url1"; then
            log "WARN" "主下载源失败，尝试备用源..."
            wget -t 3 -T 30 -O "$filename" "$url2" || error_exit "无法下载 $filename"
        fi
        log "INFO" "$filename 下载成功"
    }
    
    # 下载各个组件
    download_with_retry "nginx-${NGINX_VERSION}.tar.gz" \
        "https://nginx.org/download/nginx-${NGINX_VERSION}.tar.gz" \
        "https://mirrors.huaweicloud.com/nginx/nginx-${NGINX_VERSION}.tar.gz"
    
    download_with_retry "pcre-${PCRE_VERSION}.tar.gz" \
        "https://sourceforge.net/projects/pcre/files/pcre/${PCRE_VERSION}/pcre-${PCRE_VERSION}.tar.gz/download" \
        "https://mirrors.oneinstack.com/oneinstack/src/pcre-${PCRE_VERSION}.tar.gz"
    
    download_with_retry "openssl-${OPENSSL_VERSION}.tar.gz" \
        "https://www.openssl.org/source/openssl-${OPENSSL_VERSION}.tar.gz" \
        "https://mirrors.oneinstack.com/oneinstack/src/openssl-${OPENSSL_VERSION}.tar.gz"
    
    download_with_retry "zlib-${ZLIB_VERSION}.tar.gz" \
        "https://www.zlib.net/zlib-${ZLIB_VERSION}.tar.gz" \
        "https://mirrors.oneinstack.com/oneinstack/src/zlib-${ZLIB_VERSION}.tar.gz"
}

# 解压源码
extract_sources() {
    log "INFO" "解压源码包..."
    
    cd "$SRC_DIR" || error_exit "无法进入源码目录"
    
    tar -zxf "nginx-${NGINX_VERSION}.tar.gz" || error_exit "解压Nginx源码失败"
    tar -zxf "pcre-${PCRE_VERSION}.tar.gz" || error_exit "解压PCRE源码失败"
    tar -zxf "openssl-${OPENSSL_VERSION}.tar.gz" || error_exit "解压OpenSSL源码失败"
    tar -zxf "zlib-${ZLIB_VERSION}.tar.gz" || error_exit "解压zlib源码失败"
}

# 编译安装Nginx
compile_nginx() {
    log "INFO" "开始编译Nginx..."
    
    cd "${SRC_DIR}/nginx-${NGINX_VERSION}" || error_exit "无法进入Nginx源码目录"
    
    # 关闭debug模式
    sed -i 's@CFLAGS="$CFLAGS -g"@#CFLAGS="$CFLAGS -g"@' auto/cc/gcc
    
    # 配置编译选项
    log "INFO" "配置编译参数..."
    ./configure \
        --prefix="$NGINX_INSTALL_DIR" \
        --user="$RUN_USER" \
        --group="$RUN_GROUP" \
        --with-http_ssl_module \
        --with-http_v2_module \
        --with-http_realip_module \
        --with-http_stub_status_module \
        --with-http_gzip_static_module \
        --with-http_sub_module \
        --with-http_flv_module \
        --with-http_mp4_module \
        --with-http_gunzip_module \
        --with-http_secure_link_module \
        --with-http_auth_request_module \
        --with-stream \
        --with-stream_ssl_module \
        --with-stream_ssl_preread_module \
        --with-stream_realip_module \
        --with-pcre="../pcre-${PCRE_VERSION}" \
        --with-pcre-jit \
        --with-openssl="../openssl-${OPENSSL_VERSION}" \
        --with-zlib="../zlib-${ZLIB_VERSION}" \
        --with-file-aio \
        --with-http_addition_module \
        --with-http_random_index_module \
        || error_exit "配置编译参数失败"
    
    log "INFO" "开始编译（使用 $THREAD 个线程）..."
    make -j "$THREAD" || error_exit "编译失败"
    
    log "INFO" "安装Nginx..."
    make install || error_exit "安装失败"
}

# 验证安装
verify_installation() {
    if [[ ! -f "$NGINX_INSTALL_DIR/sbin/nginx" ]]; then
        error_exit "Nginx可执行文件不存在"
    fi
    
    if [[ ! -f "$NGINX_INSTALL_DIR/conf/nginx.conf" ]]; then
        error_exit "Nginx配置文件不存在"
    fi
    
    log "INFO" "Nginx安装验证成功"
}

# 创建systemd服务文件
create_systemd_service() {
    log "INFO" "创建systemd服务文件..."
    
    cat > /etc/systemd/system/nginx.service << 'EOF'
[Unit]
Description=The nginx HTTP and reverse proxy server
Documentation=http://nginx.org/en/docs/
After=network-online.target remote-fs.target nss-lookup.target
Wants=network-online.target

[Service]
Type=forking
PIDFile=/var/run/nginx.pid
ExecStartPre=/usr/bin/rm -f /var/run/nginx.pid
ExecStartPre=/usr/local/nginx/sbin/nginx -t
ExecStart=/usr/local/nginx/sbin/nginx
ExecReload=/bin/kill -s HUP $MAINPID
KillSignal=SIGQUIT
TimeoutStopSec=5
KillMode=mixed
PrivateTmp=true

# Limits
LimitNOFILE=1000000
LimitNPROC=1000000
LimitCORE=1000000

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload || error_exit "重新加载systemd失败"
}

# 创建Nginx配置文件
create_nginx_config() {
    log "INFO" "创建Nginx配置文件..."
    
    # 备份原配置文件
    if [[ -f "$NGINX_INSTALL_DIR/conf/nginx.conf" ]]; then
        cp "$NGINX_INSTALL_DIR/conf/nginx.conf" "$NGINX_INSTALL_DIR/conf/nginx.conf.bak"
    fi
    
    # 创建优化的nginx.conf
    cat > "$NGINX_INSTALL_DIR/conf/nginx.conf" << EOF
# Nginx主配置文件 - 优化版本
user $RUN_USER $RUN_GROUP;
worker_processes auto;
worker_cpu_affinity auto;

error_log $WWW_LOGS_DIR/error_nginx.log warn;
pid /var/run/nginx.pid;
worker_rlimit_nofile 65535;

events {
    use epoll;
    worker_connections 65535;
    multi_accept on;
    accept_mutex off;
}

http {
    include       mime.types;
    default_type  application/octet-stream;
    
    # 服务器标识
    server_tokens off;
    server_names_hash_bucket_size 128;
    server_names_hash_max_size 512;
    
    # 客户端设置
    client_header_buffer_size 32k;
    large_client_header_buffers 4 32k;
    client_max_body_size 50m;
    client_body_buffer_size 128k;
    client_header_timeout 30s;
    client_body_timeout 30s;
    
    # 发送设置
    sendfile on;
    tcp_nopush on;
    tcp_nodelay on;
    keepalive_timeout 65;
    keepalive_requests 100;
    
    # FastCGI设置
    fastcgi_connect_timeout 300;
    fastcgi_send_timeout 300;
    fastcgi_read_timeout 300;
    fastcgi_buffer_size 64k;
    fastcgi_buffers 4 64k;
    fastcgi_busy_buffers_size 128k;
    fastcgi_temp_file_write_size 256k;
    fastcgi_intercept_errors on;
    
    # Gzip压缩
    gzip on;
    gzip_vary on;
    gzip_min_length 1k;
    gzip_buffers 4 16k;
    gzip_comp_level 6;
    gzip_types
        text/plain
        text/css
        text/xml
        text/javascript
        application/json
        application/javascript
        application/xml+rss
        application/atom+xml
        image/svg+xml;
    gzip_disable "MSIE [1-6]\.";
    
    # 安全头设置
    add_header X-Frame-Options SAMEORIGIN always;
    add_header X-Content-Type-Options nosniff always;
    add_header X-XSS-Protection "1; mode=block" always;
    
    # 日志格式
    log_format main '\$remote_addr - \$remote_user [\$time_local] "\$request" '
                   '\$status \$body_bytes_sent "\$http_referer" '
                   '"\$http_user_agent" "\$http_x_forwarded_for"';
                   
    log_format json escape=json '{'
                   '"@timestamp":"\$time_iso8601",'
                   '"remote_addr":"\$remote_addr",'
                   '"request_method":"\$request_method",'
                   '"request_uri":"\$request_uri",'
                   '"status":\$status,'
                   '"body_bytes_sent":\$body_bytes_sent,'
                   '"request_time":\$request_time,'
                   '"upstream_response_time":"\$upstream_response_time",'
                   '"http_referer":"\$http_referer",'
                   '"http_user_agent":"\$http_user_agent"'
                   '}';
    
    access_log $WWW_LOGS_DIR/access_nginx.log main;
    
    # 默认服务器配置
    server {
        listen 80 default_server;
        listen [::]:80 default_server;
        server_name _;
        root $WWW_ROOT_DIR/default;
        index index.html index.htm index.php;
        
        # 安全设置
        location ~ /\. {
            deny all;
            access_log off;
            log_not_found off;
        }
        
        location ~ ^/(\.user.ini|\.ht|\.git|\.svn|\.project|LICENSE|README.md) {
            deny all;
            access_log off;
            log_not_found off;
        }
        
        # 状态页面
        location /nginx_status {
            stub_status on;
            access_log off;
            allow 127.0.0.1;
            allow ::1;
            deny all;
        }
        
        # PHP处理
        location ~ \.php$ {
            try_files \$uri =404;
            fastcgi_pass unix:/run/php/php-fpm.sock;
            fastcgi_index index.php;
            fastcgi_param SCRIPT_FILENAME \$document_root\$fastcgi_script_name;
            include fastcgi_params;
        }
        
        # 静态资源缓存
        location ~* \.(jpg|jpeg|gif|png|css|js|ico|xml)$ {
            expires 30d;
            add_header Cache-Control "public, immutable";
            access_log off;
        }
        
        # Let's Encrypt验证
        location /.well-known/acme-challenge/ {
            root $WWW_ROOT_DIR/default;
            allow all;
        }
        
        # 错误页面
        error_page 404 /404.html;
        error_page 500 502 503 504 /50x.html;
        location = /50x.html {
            root $WWW_ROOT_DIR/default;
        }
    }
    
    # 包含虚拟主机配置
    include $NGINX_INSTALL_DIR/conf/vhost/*.conf;
}
EOF
}

# 创建代理配置文件
create_proxy_config() {
    log "INFO" "创建代理配置文件..."
    
    cat > "$NGINX_INSTALL_DIR/conf/proxy.conf" << 'EOF'
# 代理配置文件
proxy_connect_timeout 300s;
proxy_send_timeout 900s;
proxy_read_timeout 900s;
proxy_buffer_size 32k;
proxy_buffers 4 64k;
proxy_busy_buffers_size 128k;
proxy_temp_file_write_size 128k;
proxy_redirect off;
proxy_hide_header Vary;
proxy_set_header Accept-Encoding '';
proxy_set_header Referer $http_referer;
proxy_set_header Cookie $http_cookie;
proxy_set_header Host $host;
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto $scheme;
proxy_set_header X-Forwarded-Host $host;
proxy_set_header X-Forwarded-Port $server_port;

# 缓存设置
proxy_cache_valid 200 302 10m;
proxy_cache_valid 301 1h;
proxy_cache_valid any 1m;
EOF
}

# 创建默认网站
create_default_site() {
    log "INFO" "创建默认网站..."
    
    cat > "$WWW_ROOT_DIR/default/index.html" << 'EOF'
<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Nginx 安装成功</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 0; padding: 20px; background: #f5f5f5; }
        .container { max-width: 800px; margin: 0 auto; background: white; padding: 40px; border-radius: 10px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }
        h1 { color: #2c3e50; text-align: center; }
        .success { color: #27ae60; text-align: center; font-size: 18px; }
        .info { background: #ecf0f1; padding: 20px; border-radius: 5px; margin: 20px 0; }
        .version { text-align: center; color: #7f8c8d; margin-top: 20px; }
    </style>
</head>
<body>
    <div class="container">
        <h1>🎉 Nginx 安装成功！</h1>
        <div class="success">
            恭喜！您的 Nginx 服务器已成功安装并运行。
        </div>
        <div class="info">
            <h3>服务器信息：</h3>
            <ul>
                <li>Nginx 版本：${NGINX_VERSION}</li>
                <li>安装路径：/usr/local/nginx</li>
                <li>配置文件：/usr/local/nginx/conf/nginx.conf</li>
                <li>网站根目录：/data/wwwroot/default</li>
                <li>日志目录：/data/wwwlogs</li>
            </ul>
        </div>
        <div class="version">
            OneInStack Panel - Nginx Installation Script v2.0
        </div>
    </div>
</body>
</html>
EOF
    
    # 设置权限
    chown -R "$RUN_USER:$RUN_GROUP" "$WWW_ROOT_DIR"
    chown -R "$RUN_USER:$RUN_GROUP" "$WWW_LOGS_DIR"
}

# 设置环境变量
setup_environment() {
    log "INFO" "设置环境变量..."
    
    # 创建软链接
    ln -sf "$NGINX_INSTALL_DIR/sbin/nginx" /usr/local/bin/nginx
    ln -sf "$NGINX_INSTALL_DIR/sbin/nginx" /usr/bin/nginx
}

# 启动服务
start_services() {
    log "INFO" "启动Nginx服务..."
    
    # 测试配置文件
    if ! "$NGINX_INSTALL_DIR/sbin/nginx" -t; then
        error_exit "Nginx配置文件测试失败"
    fi
    
    # 启用并启动服务
    systemctl enable nginx || error_exit "启用Nginx服务失败"
    systemctl start nginx || error_exit "启动Nginx服务失败"
    
    # 检查服务状态
    if systemctl is-active --quiet nginx; then
        log "INFO" "Nginx服务启动成功"
    else
        error_exit "Nginx服务启动失败"
    fi
}

# 清理临时文件
cleanup() {
    log "INFO" "清理临时文件..."
    
    cd /
    rm -rf "${SRC_DIR}/nginx-${NGINX_VERSION}"
    rm -rf "${SRC_DIR}/pcre-${PCRE_VERSION}"
    rm -rf "${SRC_DIR}/openssl-${OPENSSL_VERSION}"
    rm -rf "${SRC_DIR}/zlib-${ZLIB_VERSION}"
    rm -f "${SRC_DIR}/"*.tar.gz
}

# 显示安装信息
show_installation_info() {
    local nginx_version=$("$NGINX_INSTALL_DIR/sbin/nginx" -v 2>&1 | grep -o '[0-9]\+\.[0-9]\+\.[0-9]\+')
    
    cat << EOF

${GREEN}=============================================================================
🎉 Nginx ${nginx_version} 安装完成！
=============================================================================${NC}

${GREEN}📁 安装路径信息：${NC}
   • Nginx 安装目录: ${NGINX_INSTALL_DIR}
   • 网站根目录:     ${WWW_ROOT_DIR}
   • 日志目录:       ${WWW_LOGS_DIR}
   • 配置文件:       ${NGINX_INSTALL_DIR}/conf/nginx.conf

${GREEN}🔧 服务管理命令：${NC}
   • 启动服务:       systemctl start nginx
   • 停止服务:       systemctl stop nginx
   • 重启服务:       systemctl restart nginx
   • 重载配置:       systemctl reload nginx
   • 查看状态:       systemctl status nginx
   • 测试配置:       nginx -t

${GREEN}🌐 访问信息：${NC}
   • 本地访问:       http://localhost
   • 状态页面:       http://localhost/nginx_status

${YELLOW}⚠️  重要提示：${NC}
   • 请根据实际需求调整 ${NGINX_INSTALL_DIR}/conf/nginx.conf
   • 虚拟主机配置请放在 ${NGINX_INSTALL_DIR}/conf/vhost/ 目录
   • 建议配置 SSL 证书以启用 HTTPS

${GREEN}安装完成！${NC}

EOF
}

# 主函数
main() {
    echo -e "${GREEN}"
    cat << 'EOF'
=============================================================================
                    Nginx 安装脚本 v2.0
                    支持主流Linux发行版
=============================================================================
EOF
    echo -e "${NC}"
    
    log "INFO" "开始安装 Nginx ${NGINX_VERSION}..."
    
    # 执行安装步骤
    check_root
    detect_os
    create_directories
    create_user_group
    install_dependencies
    download_sources
    extract_sources
    compile_nginx
    verify_installation
    create_systemd_service
    create_nginx_config
    create_proxy_config
    create_default_site
    setup_environment
    start_services
    cleanup
    show_installation_info
    
    log "INFO" "Nginx 安装完成！"
}

# 执行主函数
main "$@"

`

var php = `
#!/bin/bash

# 检查操作系统类型并选择安装方法
OS=$(awk -F= '/^NAME/{print $2}' /etc/os-release)

echo "操作系统: $OS"

# 设置稳定版本的 PHP 版本
case "$1" in
    5)
        PHP_VERSION="5.6"
        ;;
    7)
        PHP_VERSION="7.4"
        ;;
    8)
        PHP_VERSION="8.1"
        ;;
    *)
        echo "请提供有效的 PHP 版本 (5, 7, 或 8)。例如: ./php.sh 5"
        exit 1
        ;;
esac

# 更新包管理器的仓库
echo "更新包列表..."
if [[ "$OS" =~ "Ubuntu" || "$OS" =~ "Debian" ]]; then
    sudo apt update -y
    sudo apt install -y software-properties-common

    # 添加 PPA 仓库，支持多个 PHP 版本
    sudo add-apt-repository -y ppa:ondrej/php
    sudo apt update -y

    # 安装指定版本的 PHP 及常用扩展
    echo "安装 PHP $PHP_VERSION 和相关扩展..."
    sudo apt install -y php$PHP_VERSION php$PHP_VERSION-cli php$PHP_VERSION-fpm php$PHP_VERSION-mysql php$PHP_VERSION-xml php$PHP_VERSION-mbstring php$PHP_VERSION-curl php$PHP_VERSION-gd php$PHP_VERSION-zip
elif [[ "$OS" =~ "CentOS" || "$OS" =~ "RHEL" ]]; then
    sudo yum update -y
    sudo yum install -y epel-release
    sudo yum install -y https://rpms.remirepo.net/enterprise/remi-release-7.rpm
    sudo yum install -y yum-utils

    # 启用 Remi 仓库并安装指定版本 PHP
    echo "安装 PHP $PHP_VERSION 和相关扩展..."
    sudo yum module enable -y php:$PHP_VERSION
    sudo yum install -y php$PHP_VERSION php$PHP_VERSION-cli php$PHP_VERSION-fpm php$PHP_VERSION-mysqlnd php$PHP_VERSION-xml php$PHP_VERSION-mbstring php$PHP_VERSION-curl php$PHP_VERSION-gd php$PHP_VERSION-zip
else
    echo "不支持的操作系统。只支持 Ubuntu/Debian/CentOS/RHEL。"
    exit 1
fi

# 启动 PHP-FPM 服务并设置为开机自启
echo "启动 PHP $PHP_VERSION FPM 服务..."
if [[ "$OS" =~ "Ubuntu" || "$OS" =~ "Debian" ]]; then
    sudo systemctl start php$PHP_VERSION-fpm
    sudo systemctl enable php$PHP_VERSION-fpm
elif [[ "$OS" =~ "CentOS" || "$OS" =~ "RHEL" ]]; then
    sudo systemctl start php-fpm
    sudo systemctl enable php-fpm
fi

# 检查 PHP 安装
echo "检查 PHP $PHP_VERSION 版本..."
php -v

# 提示安装完成
echo "PHP $PHP_VERSION 安装完成，FPM 服务已启动并设置为开机自启。"

`

var phpmyadmin = `
#!/bin/bash

# 检查是否以root用户运行
if [ "$(id -u)" -ne 0 ]; then
  echo "请以root用户或使用sudo运行此脚本。"
  exit 1
fi

# 检测操作系统类型
if [ -f /etc/os-release ]; then
  . /etc/os-release
  OS=$ID
else
  echo "无法检测操作系统类型，请手动安装 phpMyAdmin。"
  exit 1
fi

# 下载并安装 phpMyAdmin
install_phpmyadmin() {
  PHP_MYADMIN_VERSION="5.2.1"
  DOWNLOAD_URL="https://www.phpmyadmin.net/downloads/phpMyAdmin-${PHP_MYADMIN_VERSION}-all-languages.zip"

  echo "下载 phpMyAdmin..."
  wget -q $DOWNLOAD_URL -O /tmp/phpmyadmin.zip

  echo "解压 phpMyAdmin..."
  unzip -qo /tmp/phpmyadmin.zip -d /usr/share/
  mv /usr/share/phpMyAdmin-${PHP_MYADMIN_VERSION}-all-languages /usr/share/phpmyadmin

  echo "设置权限..."
  chown -R root:root /usr/share/phpmyadmin
  chmod -R 755 /usr/share/phpmyadmin
  find /usr/share/phpmyadmin -type d -exec chmod 755 {} \;
  find /usr/share/phpmyadmin -type f -exec chmod 644 {} \;

  echo "清理临时文件..."
  rm -f /tmp/phpmyadmin.zip
}

# 配置 Nginx
configure_nginx() {
  echo "配置 Nginx..."
  cat > /etc/nginx/sites-enabled/phpmyadmin.conf <<EOF
server {
    listen 8080;
    server_name localhost; # 使用提供的地址

    location /phpmyadmin {
        root /usr/share/;
        index index.php;
        location ~ ^/phpmyadmin/(.+\.php)$ {
            fastcgi_pass unix:/run/php/php7.4-fpm.sock;
            fastcgi_index index.php;
            include fastcgi_params;
            fastcgi_param SCRIPT_FILENAME /usr/share/phpmyadmin/$1;
        }

        location ~* ^/phpmyadmin/(.+\.(jpg|jpeg|gif|css|png|js|ico|html|xml|txt))$ {
            root /usr/share/;
        }
    }
}
EOF
  systemctl restart nginx
}

# 主脚本逻辑
echo "开始安装 phpMyAdmin..."
install_phpmyadmin
configure_nginx

IP_ADDRESS=$(hostname -I | awk '{print $1}')
echo "phpMyAdmin 已成功安装！您可以通过以下地址访问："
echo "http://$IP_ADDRESS:8080/phpmyadmin"

`

var java = `
#!/bin/bash

# 默认 JDK 版本
DEFAULT_JAVA_VERSION="11"

# 检查当前操作系统类型
OS=$(lsb_release -i | awk '{print $3}')

# 解析命令行参数
while getopts "v:" opt; do
  case ${opt} in
    v)
      JAVA_VERSION=$OPTARG
      ;;
    *)
      echo "Usage: $0 [-v version]"
      exit 1
      ;;
  esac
done

# 如果没有指定版本，使用默认版本
JAVA_VERSION=${JAVA_VERSION:-$DEFAULT_JAVA_VERSION}

# 设置 Java 安装路径（可以修改为你想要的路径）
JAVA_HOME_DIR="/usr/lib/jvm/java-${JAVA_VERSION}-openjdk-amd64"

# 安装 Java 的函数
install_java() {
    echo "开始安装 OpenJDK ${JAVA_VERSION} ..."
    case $OS in
        "Ubuntu"|"Debian")
            sudo apt update
            sudo apt install -y openjdk-${JAVA_VERSION}-jdk
            ;;
        "CentOS"|"RedHatEnterpriseServer")
            sudo yum install -y java-${JAVA_VERSION}-openjdk
            ;;
        "Fedora")
            sudo dnf install -y java-${JAVA_VERSION}-openjdk
            ;;
        *)
            echo "不支持的操作系统: $OS"
            exit 1
            ;;
    esac
}

# 设置 JAVA 环境变量
set_java_env() {
    echo "设置 JAVA 环境变量 ..."
    
    # 检查 JAVA_HOME 是否已设置
    if ! grep -q "JAVA_HOME" /etc/profile.d/java.sh; then
        echo "JAVA_HOME 未设置，正在设置 ..."
        echo "export JAVA_HOME=$JAVA_HOME_DIR" | sudo tee /etc/profile.d/java.sh
        echo "export PATH=\$JAVA_HOME/bin:\$PATH" | sudo tee -a /etc/profile.d/java.sh
        sudo chmod +x /etc/profile.d/java.sh
    else
        echo "JAVA_HOME 已经设置，跳过设置步骤"
    fi
    
    # 加载新的配置
    source /etc/profile.d/java.sh
}

# 验证 Java 安装是否成功
verify_java_install() {
    echo "验证 Java 安装 ..."
    java -version
    if [ $? -ne 0 ]; then
        echo "Java 安装失败，请检查日志！"
        exit 1
    else
        echo "Java 安装成功！"
    fi
}

# 主程序
if ! java -version &>/dev/null; then
    # 如果 Java 未安装，则进行安装
    install_java
else
    echo "Java 已安装，跳过安装步骤"
fi

# 设置环境变量
set_java_env

# 验证安装
verify_java_install

`

var openJDK11 = `
#!/bin/bash

# Detect OS type
if [ -f /etc/redhat-release ]; then
    OS_FAMILY='rhel'
elif [ -f /etc/debian_version ]; then
    if grep -iq ubuntu /etc/os-release; then
        OS_FAMILY='ubuntu'
        Ubuntu_ver=$(lsb_release -rs | cut -d. -f1)
    else
        OS_FAMILY='debian'
    fi
else
    echo "Unsupported OS. Exiting."
    exit 1
fi

SYS_ARCH=$(dpkg --print-architecture 2>/dev/null || uname -m)

# Install OpenJDK 11
if [ "${OS_FAMILY}" == 'rhel' ]; then
    yum -y install java-11-openjdk-devel
    JAVA_HOME=/usr/lib/jvm/java-11-openjdk
elif [ "${OS_FAMILY}" == 'debian' ]; then
    apt-get update
    apt-get --no-install-recommends -y install openjdk-11-jdk
    JAVA_HOME=/usr/lib/jvm/java-11-openjdk-${SYS_ARCH}
elif [ "${OS_FAMILY}" == 'ubuntu' ]; then
    if [[ "${Ubuntu_ver}" =~ ^16$ ]]; then
        wget -qO - https://mirrors.tuna.tsinghua.edu.cn/Adoptium/deb/Release.key | apt-key add -
        apt-add-repository --yes https://mirrors.tuna.tsinghua.edu.cn/Adoptium/deb
        apt update
        apt-get --no-install-recommends -y install temurin-11-jdk
        JAVA_HOME=/usr/lib/jvm/temurin-11-jdk-${SYS_ARCH}
    else
        apt-get update
        apt-get --no-install-recommends -y install openjdk-11-jdk
        JAVA_HOME=/usr/lib/jvm/java-11-openjdk-${SYS_ARCH}
    fi
fi

# Verify installation
if [ -e "${JAVA_HOME}/bin/java" ]; then
    cat > /etc/profile.d/openjdk.sh << EOF
export JAVA_HOME=${JAVA_HOME}
export CLASSPATH=\$JAVA_HOME/lib/tools.jar:\$JAVA_HOME/lib/dt.jar:\$JAVA_HOME/lib
export PATH=\$JAVA_HOME/bin:\$PATH
EOF

    source /etc/profile.d/openjdk.sh
    echo "OpenJDK 11 installation completed successfully."
else
    echo "OpenJDK 11 installation failed."
    grep -Ew 'NAME|ID|ID_LIKE|VERSION_ID|PRETTY_NAME' /etc/os-release
    exit 1
fi

`

var openJDK17 = `
#!/bin/bash

# Detect OS type and version
if [ -f /etc/redhat-release ]; then
    OS_FAMILY='rhel'
    RHEL_ver=$(rpm -q --queryformat '%{VERSION}' centos-release || rpm -q --queryformat '%{VERSION}' redhat-release-server)
elif [ -f /etc/debian_version ]; then
    if grep -iq ubuntu /etc/os-release; then
        OS_FAMILY='ubuntu'
        Ubuntu_ver=$(lsb_release -rs | cut -d. -f1)
    else
        OS_FAMILY='debian'
        Debian_ver=$(lsb_release -rs | cut -d. -f1)
    fi
else
    echo "Unsupported OS. Exiting."
    exit 1
fi

SYS_ARCH=$(dpkg --print-architecture 2>/dev/null || uname -m)

# Install OpenJDK 17
if [ "${OS_FAMILY}" == 'rhel' ]; then
    if [[ "${RHEL_ver}" =~ ^7$ ]]; then
        cat > /etc/yum.repos.d/adoptium.repo << EOF
[Adoptium]
name=Adoptium
baseurl=https://mirrors.tuna.tsinghua.edu.cn/Adoptium/rpm/rhel\$releasever-\$basearch/
enabled=1
gpgcheck=0
EOF
        yum -y install temurin-17-jdk
        JAVA_HOME=/usr/lib/jvm/temurin-17-jdk
    else
        yum -y install java-17-openjdk-devel
        JAVA_HOME=/usr/lib/jvm/java-17-openjdk
    fi
elif [ "${OS_FAMILY}" == 'debian' ]; then
    apt-get update
    if [[ "${Debian_ver}" =~ ^9$|^10$ ]]; then
        wget -qO - https://mirrors.tuna.tsinghua.edu.cn/Adoptium/deb/Release.key | apt-key add -
        apt-add-repository --yes https://mirrors.tuna.tsinghua.edu.cn/Adoptium/deb
        apt update
        apt-get --no-install-recommends -y install temurin-17-jdk
        JAVA_HOME=/usr/lib/jvm/temurin-17-jdk-${SYS_ARCH}
    else
        apt-get --no-install-recommends -y install openjdk-17-jdk
        JAVA_HOME=/usr/lib/jvm/java-17-openjdk-${SYS_ARCH}
    fi
elif [ "${OS_FAMILY}" == 'ubuntu' ]; then
    apt-get update
    if [[ "${Ubuntu_ver}" =~ ^16$ ]]; then
        wget -qO - https://mirrors.tuna.tsinghua.edu.cn/Adoptium/deb/Release.key | apt-key add -
        apt-add-repository --yes https://mirrors.tuna.tsinghua.edu.cn/Adoptium/deb
        apt update
        apt-get --no-install-recommends -y install temurin-17-jdk
        JAVA_HOME=/usr/lib/jvm/temurin-17-jdk-${SYS_ARCH}
    else
        apt-get --no-install-recommends -y install openjdk-17-jdk
        JAVA_HOME=/usr/lib/jvm/java-17-openjdk-${SYS_ARCH}
    fi
fi

# Verify installation
if [ -e "${JAVA_HOME}/bin/java" ]; then
    cat > /etc/profile.d/openjdk.sh << EOF
export JAVA_HOME=${JAVA_HOME}
export CLASSPATH=\$JAVA_HOME/lib
export PATH=\$JAVA_HOME/bin:\$PATH
EOF

    source /etc/profile.d/openjdk.sh
    echo "OpenJDK 17 installation completed successfully."
else
    echo "OpenJDK 17 installation failed."
    grep -Ew 'NAME|ID|ID_LIKE|VERSION_ID|PRETTY_NAME' /etc/os-release
    exit 1
fi



`

var openJDK18 = `
#!/bin/bash

# Detect OS type and version
if [ -f /etc/redhat-release ]; then
    OS_FAMILY='rhel'
    RHEL_ver=$(rpm -q --queryformat '%{VERSION}' centos-release || rpm -q --queryformat '%{VERSION}' redhat-release-server)
elif [ -f /etc/debian_version ]; then
    if grep -iq ubuntu /etc/os-release; then
        OS_FAMILY='ubuntu'
        Ubuntu_ver=$(lsb_release -rs | cut -d. -f1)
    else
        OS_FAMILY='debian'
        Debian_ver=$(lsb_release -rs | cut -d. -f1)
    fi
else
    echo "Unsupported OS. Exiting."
    exit 1
fi

SYS_ARCH=$(dpkg --print-architecture 2>/dev/null || uname -m)

# Install OpenJDK 18
if [ "${OS_FAMILY}" == 'rhel' ]; then
    if [[ "${RHEL_ver}" =~ ^7$ ]]; then
        cat > /etc/yum.repos.d/adoptium.repo << EOF
[Adoptium]
name=Adoptium
baseurl=https://mirrors.tuna.tsinghua.edu.cn/Adoptium/rpm/rhel\$releasever-\$basearch/
enabled=1
gpgcheck=0
EOF
        yum -y install temurin-18-jdk
        JAVA_HOME=/usr/lib/jvm/temurin-18-jdk
    else
        yum -y install java-18-openjdk-devel
        JAVA_HOME=/usr/lib/jvm/java-18-openjdk
    fi
elif [ "${OS_FAMILY}" == 'debian' ]; then
    apt-get update
    if [[ "${Debian_ver}" =~ ^9$|^10$ ]]; then
        wget -qO - https://mirrors.tuna.tsinghua.edu.cn/Adoptium/deb/Release.key | apt-key add -
        apt-add-repository --yes https://mirrors.tuna.tsinghua.edu.cn/Adoptium/deb
        apt update
        apt-get --no-install-recommends -y install temurin-18-jdk
        JAVA_HOME=/usr/lib/jvm/temurin-18-jdk-${SYS_ARCH}
    else
        apt-get --no-install-recommends -y install openjdk-18-jdk
        JAVA_HOME=/usr/lib/jvm/java-18-openjdk-${SYS_ARCH}
    fi
elif [ "${OS_FAMILY}" == 'ubuntu' ]; then
    apt-get update
    if [[ "${Ubuntu_ver}" =~ ^16$ ]]; then
        wget -qO - https://mirrors.tuna.tsinghua.edu.cn/Adoptium/deb/Release.key | apt-key add -
        apt-add-repository --yes https://mirrors.tuna.tsinghua.edu.cn/Adoptium/deb
        apt update
        apt-get --no-install-recommends -y install temurin-18-jdk
        JAVA_HOME=/usr/lib/jvm/temurin-18-jdk-${SYS_ARCH}
    else
        apt-get --no-install-recommends -y install openjdk-18-jdk
        JAVA_HOME=/usr/lib/jvm/java-18-openjdk-${SYS_ARCH}
    fi
fi

# Verify installation
if [ -e "${JAVA_HOME}/bin/java" ]; then
    cat > /etc/profile.d/openjdk.sh << EOF
export JAVA_HOME=${JAVA_HOME}
export CLASSPATH=\$JAVA_HOME/lib
export PATH=\$JAVA_HOME/bin:\$PATH
EOF

    source /etc/profile.d/openjdk.sh
    echo "OpenJDK 18 installation completed successfully."
else
    echo "OpenJDK 18 installation failed."
    grep -Ew 'NAME|ID|ID_LIKE|VERSION_ID|PRETTY_NAME' /etc/os-release
    exit 1
fi

`

var openresty = `
#!/bin/bash

# 安装重试次数
MAX_RETRIES=3
# 重试间隔时间（秒）
RETRY_DELAY=5

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m' # No Color

# 检查root权限
if [ "$EUID" -ne 0 ]; then
    echo -e "${RED}错误：请使用root权限或sudo运行此脚本${NC}"
    exit 1
fi

# 带重试功能的命令执行函数
retry_command() {
    local command="$1"
    local description="$2"
    local attempt=1
    
    until eval "$command"; do
        if [ $attempt -ge $MAX_RETRIES ]; then
            echo -e "${RED}$description 失败，已达到最大重试次数${NC}"
            return 1
        fi
        echo -e "${YELLOW}$description 失败，${attempt}/${MAX_RETRIES} 重试...${NC}"
        sleep $RETRY_DELAY
        ((attempt++))
    done
    return 0
}

# 检测系统类型
if [ -f /etc/os-release ]; then
    . /etc/os-release
    OS=$ID
    VERSION=$VERSION_ID
else
    echo -e "${RED}无法检测操作系统类型${NC}"
    exit 1
fi

# 安装必要工具
install_tools() {
    case "$OS" in
        ubuntu|debian)
            retry_command "apt-get update" "更新包列表" && \
            retry_command "apt-get install -y wget gnupg" "安装依赖工具"
            ;;
        centos|almalinux|fedora)
            retry_command "yum install -y wget" "安装依赖工具"
            ;;
        *)
            echo -e "${RED}不支持的Linux发行版: $OS${NC}"
            exit 1
            ;;
    esac
}

# 添加OpenResty仓库
add_repo() {
    case "$OS" in
        ubuntu|debian)
            if [ ! -f /etc/apt/sources.list.d/openresty.list ]; then
                retry_command "wget -qO - https://openresty.org/package/pubkey.gpg | apt-key add -" "导入GPG密钥" && \
                retry_command "echo \"deb http://openresty.org/package/ubuntu $(lsb_release -sc) main\" > /etc/apt/sources.list.d/openresty.list" "添加APT仓库"
            fi
            ;;
        centos|almalinux)
            if [ ! -f /etc/yum.repos.d/openresty.repo ]; then
                retry_command "wget -qO /etc/yum.repos.d/openresty.repo https://openresty.org/package/centos/openresty.repo" "添加YUM仓库"
            fi
            ;;
        fedora)
            if [ ! -f /etc/yum.repos.d/openresty.repo ]; then
                retry_command "wget -qO /etc/yum.repos.d/openresty.repo https://openresty.org/package/fedora/openresty.repo" "添加Fedora仓库"
            fi
            ;;
    esac
}

# 执行安装流程
install_openresty() {
    echo -e "${GREEN}开始安装OpenResty...${NC}"
    
    # 安装依赖工具
    if ! install_tools; then
        echo -e "${RED}依赖工具安装失败${NC}"
        exit 1
    fi
    
    # 添加仓库
    if ! add_repo; then
        echo -e "${RED}仓库配置失败${NC}"
        exit 1
    fi
    
    # 安装OpenResty
    case "$OS" in
        ubuntu|debian)
            retry_command "apt-get update" "更新包列表" && \
            retry_command "apt-get install -y openresty" "安装OpenResty"
            ;;
        centos|almalinux|fedora)
            retry_command "yum install -y openresty" "安装OpenResty"
            ;;
    esac
    
    if [ $? -eq 0 ]; then
        echo -e "${GREEN}OpenResty 安装成功！${NC}"
        echo -e "运行命令启动服务: systemctl start openresty"
    else
        echo -e "${RED}OpenResty 安装失败${NC}"
        exit 1
    fi
}

# 主执行流程
install_openresty

`

//func runInstall(params *input.InstallationParams) (string, error) {
//	err := downloadshell()
//	if err != nil {
//		return "", err
//	}
//
//	// 构建命令行参数列表
//	cmdArgs := params.BuildCmdArgs()
//
//	// 添加执行权限
//	dirPath := "./oneinstack/oneinstack/include"
//	err = utils.SetExecPermissions(dirPath)
//	if err != nil {
//		return "", fmt.Errorf("设置 include 目录下文件的执行权限失败: %v", err)
//	}
//
//	scriptPath := "./oneinstack/oneinstack/install.sh"
//	err = os.Chmod(scriptPath, 0755)
//	if err != nil {
//		return "", fmt.Errorf("无法设置脚本执行权限: %v", err)
//	}
//
//	cmdInstall := exec.Command("./oneinstack/oneinstack/install.sh", cmdArgs...)
//
//	logFileName := "install_" + time.Now().Format("2006-01-02_15-04-05") + ".log"
//	logFile, err := os.Create(logFileName)
//	if err != nil {
//		return "", fmt.Errorf("无法创建日志文件: %v", err)
//	}
//	defer logFile.Close()
//
//	cmdInstall.Stdout = logFile
//	cmdInstall.Stderr = logFile
//	err = cmdInstall.Start()
//	if err != nil {
//		return "", err
//	}
//	go func() {
//		err = cmdInstall.Wait()
//		if err != nil {
//			fmt.Println("cmd wait err:" + fmt.Sprintf("%v", err))
//		}
//	}()
//
//	return logFileName, nil
//}
//

//func buildIParams(p *input.InstallParams) *input.InstallationParams {
//	ps := &input.InstallationParams{}
//	switch p.Key {
//	case "webserver":
//		ps.NginxOption = p.Version
//	case "db":
//		ps.DBOption = p.Version
//		ps.DBRootPWD = p.Pwd
//	case "redis":
//		ps.Redis = true
//	case "php":
//		if p.Version == "5.6" {
//			ps.PHPOption = "4"
//		}
//		if p.Version == "7.0" {
//			ps.PHPOption = "5"
//		}
//		if p.Version == "8.0" {
//			ps.PHPOption = "10"
//		}
//	case "java":
//		ps.JDKOption = p.Version
//	}
//	return ps
//}
