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