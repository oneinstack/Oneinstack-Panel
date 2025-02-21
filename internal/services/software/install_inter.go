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
	script, err := ps.getScript()
	if err != nil {
		return "", err
	}
	fn, err := ps.createShScript(script, ps.BashParams.Key+ps.BashParams.Version+".sh")
	if err != nil {
		return "", err
	}
	if ps.Remote {
		args, err := ps.buildRemoteArgs()
		if err != nil {
			return "", err
		}
		return ps.executeShScriptRemote(fn, sy, args...)
	} else {
		return ps.executeShScriptLocal(fn, sy)
	}
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

func (ps InstallOP) executeShScriptLocal(fn string, sy bool) (string, error) {
	switch ps.BashParams.Key {
	case "webserver":
		return ps.executeShScript(fn, sy)
	case "db":
		return ps.executeShScript(fn, sy, "-p", ps.BashParams.Pwd, "-P", "3306")
	case "redis":
		if ps.BashParams.Version == "6.2.0" {
			return ps.executeShScript(fn, sy, "6")
		}
		if ps.BashParams.Version == "7.0.5" {
			return ps.executeShScript(fn, sy, "7")
		}
		return "", fmt.Errorf("未知的redis类型")
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
		bash = php
	case "java":
		bash = java
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

	logFileName := "install_" + scriptName + time.Now().Format("2006-01-02_15-04-05") + ".log"
	logFile, err := os.Create(logFileName)
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
	tx := app.DB().Where("key = ? and version", ps.BashParams.Key, ps.BashParams.Version).Updates(&models.Software{Status: models.Soft_Status_Ing, Log: logFileName})
	if tx.Error != nil {
		fmt.Println(tx.Error.Error())
	}
	if sync {
		fmt.Println("cmd running" + scriptName)
		err = cmd.Wait()
		fmt.Println("cmd done" + scriptName)
		if err != nil {
			fmt.Println("cmd wait err:" + fmt.Sprintf("%v", err))
			app.DB().Where("key = ? and version", ps.BashParams.Key, ps.BashParams.Version).Updates(&models.Software{Status: models.Soft_Status_Err})
		}
		app.DB().Where("key = ? and version", ps.BashParams.Key, ps.BashParams.Version).Updates(&models.Software{Status: models.Soft_Status_Suc})
		return logFileName, nil
	}

	go func(bp *input.InstallParams) {
		defer func() {
			if err := recover(); err != nil {
				log.Println("InstallParams panic error:", err)
			}
		}()
		fmt.Println("cmd running" + scriptName)
		err = cmd.Wait()
		fmt.Println("cmd done" + scriptName)
		defer func() {
			if err != nil {
				fmt.Println("cmd wait err:" + fmt.Sprintf("%v", err))
				app.DB().Where("key = ? and version", ps.BashParams.Key, ps.BashParams.Version).Updates(&models.Software{Status: models.Soft_Status_Err})
				return
			}
			app.DB().Where("key = ? and version", ps.BashParams.Key, ps.BashParams.Version).Updates(&models.Software{Status: models.Soft_Status_Suc})
		}()
	}(ps.BashParams)
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

# 函数：显示帮助信息
usage() {
  echo "用法: $0 -p <root_password>"
  exit 1
}

# 解析命令行参数
while getopts "p:" opt; do
  case "$opt" in
    p) ROOT_PASSWORD="$OPTARG" ;;  # 设置 root 密码
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

# 默认参数
MYSQL_VERSION="8.0"
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
    yum -y install wget lsb-release gnupg
  elif [ "$PM" == "apt" ]; then
    apt update
    apt -y install wget lsb-release gnupg
  fi
}

# 导入 MySQL GPG 公钥
Import_MySQL_GPG_Key() {
  echo "导入 MySQL GPG 公钥..."
  wget -q https://dev.mysql.com/get/mysql-apt-config_0.8.17-1_all.deb
  dpkg -i mysql-apt-config_0.8.17-1_all.deb
  wget -q http://repo.mysql.com/RPM-GPG-KEY-mysql-2022
  apt-key adv --fetch-keys http://repo.mysql.com/RPM-GPG-KEY-mysql-2022
  apt-get update
}

# 安装 MySQL 8.0
Install_MySQL() {
  echo "安装 MySQL 8.0..."
  if [ "$PM" == "yum" ]; then
    yum -y install mysql-server
  elif [ "$PM" == "apt" ]; then
    apt -y install mysql-server
  fi
}

# 启动 MySQL 服务
Start_MySQL() {
  echo "启动 MySQL 服务..."
  systemctl start mysql
  systemctl enable mysql
}

# 修改 root 密码（无交互）
Change_Root_Password() {
  echo "修改 root 密码..."
  mysql -e "ALTER USER 'root'@'localhost' IDENTIFIED BY '${MYSQL_ROOT_PASSWORD}';"
}

# 配置防火墙
Configure_Firewall() {
  echo "配置防火墙..."
  if [ "$PM" == "yum" ]; then
    firewall-cmd --zone=public --add-port=${MYSQL_PORT}/tcp --permanent &>/dev/null
    firewall-cmd --reload &>/dev/null
  elif [ "$PM" == "apt" ]; then
    ufw allow ${MYSQL_PORT}/tcp &>/dev/null
  fi
}

# 配置 MySQL 安全设置（禁用匿名用户，删除测试数据库等）
Secure_MySQL() {
  echo "配置 MySQL 安全设置..."
  mysql -e "DELETE FROM mysql.db WHERE Db='test' OR Db='test\_%';"
  mysql -e "DROP USER IF EXISTS ''@'localhost';"
  mysql -e "DROP USER IF EXISTS ''@'$(hostname)';"
  mysql -e "FLUSH PRIVILEGES;"
}

# 主函数
Main() {
  Detect_OS
  Install_Dependencies
  Import_MySQL_GPG_Key
  Install_MySQL
  Start_MySQL
  Change_Root_Password
  Configure_Firewall
  Secure_MySQL
  echo "MySQL 8.0 安装完成，root 密码已设置为: ${MYSQL_ROOT_PASSWORD}, 端口: ${MYSQL_PORT}"
}

# 执行主函数
Main

`

var redis = `
#!/bin/bash

# 脚本名称：install_redis.sh
# 用途：从源码安装 Redis 6 或 Redis 7，适配主流 Linux 发行版

# 检查是否有 root 权限
if [[ $EUID -ne 0 ]]; then
   echo "请使用 root 权限运行此脚本" 
   exit 1
fi

# 检查参数是否传递正确
if [ -z "$1" ]; then
    echo "使用方法：$0 {6|7}"
    echo "6: 安装 Redis 6"
    echo "7: 安装 Redis 7"
    exit 1
fi

# 检测发行版
if [[ -f /etc/os-release ]]; then
    . /etc/os-release
    OS=$ID
else
    echo "无法检测操作系统类型，脚本仅支持 Ubuntu/Debian 和 CentOS/RHEL"
    exit 1
fi

# 安装依赖
echo "正在安装依赖..."
if [[ "$OS" == "ubuntu" || "$OS" == "debian" ]]; then
    apt-get update && apt-get install -y build-essential tcl wget
elif [[ "$OS" == "centos" || "$OS" == "rhel" ]]; then
    yum groupinstall -y "Development Tools"
    yum install -y tcl wget
else
    echo "当前操作系统不受支持"
    exit 1
fi

# 设置版本
VERSION="$1"

# 下载 Redis 源码
if [ "$VERSION" == "6" ]; then
    echo "正在下载 Redis 6.x 源码..."
    wget https://mirrors.huaweicloud.com/redis/redis-6.2.0.tar.gz -O /tmp/redis-6.tar.gz
    tar -zxvf /tmp/redis-6.tar.gz -C /tmp
    cd /tmp/redis-6.2.0
elif [ "$VERSION" == "7" ]; then
    echo "正在下载 Redis 7.x 源码..."
    wget https://mirrors.huaweicloud.com/redis/redis-7.0.5.tar.gz -O /tmp/redis-7.tar.gz
    tar -zxvf /tmp/redis-7.tar.gz -C /tmp
    cd /tmp/redis-7.0.5
else
    echo "无效的版本号：$VERSION。请指定 6 或 7"
    exit 1
fi

# 编译和安装 Redis
echo "正在编译 Redis..."
make
make install

# 配置 Redis
echo "正在配置 Redis..."
cp /tmp/redis-*/redis.conf /etc/redis.conf

# 创建 Redis 用户和数据目录
if ! id "redis" &>/dev/null; then
    useradd -r -s /bin/false redis
fi
mkdir -p /var/lib/redis
chown redis:redis /var/lib/redis

# 创建 Redis 启动脚本
cat > /etc/systemd/system/redis.service <<EOF
[Unit]
Description=Redis In-Memory Data Store
After=network.target

[Service]
ExecStart=/usr/local/bin/redis-server /etc/redis.conf
ExecStop=/usr/local/bin/redis-cli shutdown
User=redis
Group=redis
WorkingDirectory=/var/lib/redis
Restart=always

[Install]
WantedBy=multi-user.target
EOF

# 设置 Redis 服务为开机自启并启动
systemctl enable redis
systemctl start redis

# 清理安装文件
rm -rf /tmp/redis-*

echo "Redis $VERSION 安装完成！"

`

var nginx = `
#!/bin/bash

# 检查是否有 root 权限
if [[ $EUID -ne 0 ]]; then
   echo "请使用 root 权限运行此脚本" 
   exit 1
fi

# 检测操作系统类型
OS=$(awk -F= '/^ID=/{print $2}' /etc/os-release | tr -d '"')
echo "检测到操作系统为 $OS"

# 定义安装依赖的函数
install_dependencies() {
    echo "正在安装依赖..."
    case $OS in
        ubuntu | debian)
            apt-get update && apt-get install -y build-essential libpcre3 libpcre3-dev libssl-dev zlib1g-dev wget
            ;;
        centos | rhel | rocky | almalinux | fedora)
            yum groupinstall -y "Development Tools"
            yum install -y pcre pcre-devel openssl-devel zlib-devel wget
            ;;
        *)
            echo "未支持的操作系统: $OS"
            exit 1
            ;;
    esac
}

# 调用安装依赖函数
install_dependencies

# 创建 nginx 用户和组
echo "正在创建 nginx 用户和组..."
id -u nginx &>/dev/null || useradd -r -s /sbin/nologin nginx

# 下载 Nginx 源码
NGINX_VERSION="1.24.0"
echo "正在从国内源下载 Nginx $NGINX_VERSION 源码..."
wget https://mirrors.huaweicloud.com/nginx/nginx-$NGINX_VERSION.tar.gz -O /tmp/nginx.tar.gz
tar -zxvf /tmp/nginx.tar.gz -C /tmp
cd /tmp/nginx-$NGINX_VERSION

# 编译和安装 Nginx
echo "正在编译 Nginx..."
./configure \
  --prefix=/usr/local/nginx \
  --conf-path=/etc/nginx/nginx.conf \
  --sbin-path=/usr/local/nginx/sbin/nginx \
  --error-log-path=/var/log/nginx/error.log \
  --http-log-path=/var/log/nginx/access.log \
  --pid-path=/run/nginx.pid \
  --lock-path=/var/lock/nginx.lock \
  --http-client-body-temp-path=/var/lib/nginx/body \
  --http-proxy-temp-path=/var/lib/nginx/proxy \
  --http-fastcgi-temp-path=/var/lib/nginx/fastcgi \
  --http-uwsgi-temp-path=/var/lib/nginx/uwsgi \
  --http-scgi-temp-path=/var/lib/nginx/scgi \
  --with-http_ssl_module \
  --with-http_v2_module \
  --with-pcre

make
make install

# 配置默认的 nginx.conf
echo "正在创建 nginx 配置文件..."
mkdir -p /etc/nginx/sites-enabled
cat > /etc/nginx/nginx.conf << 'EOF'
user www-data;
worker_processes  1;

#error_log  logs/error.log;
#error_log  logs/error.log  notice;
#error_log  logs/error.log  info;

#pid        logs/nginx.pid;


events {
    worker_connections  1024;
}


http {
    include       mime.types;
    include /etc/nginx/sites-enabled/*;
    default_type  application/octet-stream;

    #log_format  main  '$remote_addr - $remote_user [$time_local] "$request" '
    #                  '$status $body_bytes_sent "$http_referer" '
    #                  '"$http_user_agent" "$http_x_forwarded_for"';

    #access_log  logs/access.log  main;

    sendfile        on;
    #tcp_nopush     on;

    #keepalive_timeout  0;
    keepalive_timeout  65;

    #gzip  on;

    server {
        listen       80;
        server_name  localhost;

        #charset koi8-r;

        #access_log  logs/host.access.log  main;

        location / {
            root   html;
            index  index.html index.htm;
        }

        #error_page  404              /404.html;

        # redirect server error pages to the static page /50x.html
        #
        error_page   500 502 503 504  /50x.html;
        location = /50x.html {
            root   html;
        }

        # proxy the PHP scripts to Apache listening on 127.0.0.1:80
        #
        #location ~ \.php$ {
        #    proxy_pass   http://127.0.0.1;
        #}

        # pass the PHP scripts to FastCGI server listening on 127.0.0.1:9000
        #
        #location ~ \.php$ {
        #    root           html;
        #    fastcgi_pass   127.0.0.1:9000;
        #    fastcgi_index  index.php;
        #    fastcgi_param  SCRIPT_FILENAME  /scripts$fastcgi_script_name;
        #    include        fastcgi_params;
        #}

        # deny access to .htaccess files, if Apache's document root
        # concurs with nginx's one
        #
        #location ~ /\.ht {
        #    deny  all;
        #}
    }


    # another virtual host using mix of IP-, name-, and port-based configuration
    #
    #server {
    #    listen       8000;
    #    listen       somename:8080;
    #    server_name  somename  alias  another.alias;

    #    location / {
    #        root   html;
    #        index  index.html index.htm;
    #    }
    #}


    # HTTPS server
    #
    #server {
    #    listen       443 ssl;
    #    server_name  localhost;

    #    ssl_certificate      cert.pem;
    #    ssl_certificate_key  cert.key;

    #    ssl_session_cache    shared:SSL:1m;
    #    ssl_session_timeout  5m;

    #    ssl_ciphers  HIGH:!aNULL:!MD5;
    #    ssl_prefer_server_ciphers  on;

    #    location / {
    #        root   html;
    #        index  index.html index.htm;
    #    }
    #}

}
EOF


# 启动 Nginx 服务
echo "启动 Nginx 服务..."
nginx

# 配置 nginx 环境变量
echo "正在将 nginx 添加到环境变量中..."
ln -sf /usr/local/nginx/sbin/nginx /usr/bin/nginx

# 清理安装文件
echo "清理临时安装文件..."
rm -rf /tmp/nginx*

# 输出安装信息
echo "Nginx $NGINX_VERSION 安装完成！"
echo "默认配置文件位于 /etc/nginx/nginx.conf"
echo "站点配置目录为 /etc/nginx/sites-available 和 /etc/nginx/sites-enabled"

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
