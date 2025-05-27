#!/bin/bash

# 创建/usr/local/one/src目录
if [ ! -d "/usr/local/one/src" ]; then
  mkdir -p /usr/local/one/src
fi
oneinstack_dir=/usr/local/one
nginx_install_dir=/usr/local/nginx
www_root_dir=/data/wwwroot
www_logs_dir=/data/wwwlogs
THREAD=$(grep 'processor' /proc/cpuinfo | sort -u | wc -l)
run_group='www'
run_user='www'

# 检查是否有 root 权限
if [[ $EUID -ne 0 ]]; then
   echo "请使用 root 权限运行此脚本"
   exit 1
fi

# 判断安装oneinstack_dir目录是否存在 不存在则创建
if [ ! -d ${nginx_install_dir} ]; then
  mkdir -p ${nginx_install_dir}
fi

# 判断安装www_root_dir目录是否存在 不存在则创建
if [ ! -d ${www_root_dir} ]; then
  mkdir -p ${www_root_dir}
fi

# 判断安装www_logs_dir目录是否存在 不存在则创建
if [ ! -d ${www_logs_dir} ]; then
  mkdir -p ${www_logs_dir}
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

pushd ${oneinstack_dir}/src > /dev/null
  id -g ${run_group} >/dev/null 2>&1
  [ $? -ne 0 ] && groupadd ${run_group}
  id -u ${run_user} >/dev/null 2>&1
  [ $? -ne 0 ] && useradd -g ${run_group} -M -s /sbin/nologin ${run_user}

# 调用安装依赖函数
install_dependencies

# 创建 nginx 用户和组
echo "正在创建 nginx 用户和组..."
id -u nginx &>/dev/null || useradd -r -s /sbin/nologin nginx

# 下载 Nginx 源码
NGINX_VERSION="1.24.0"
PCRE_VERSION="8.45"
OPENSSL_VERSION="1.1.1w"
echo "正在从国内源下载 Nginx $NGINX_VERSION 源码..."
wget https://mirrors.huaweicloud.com/nginx/nginx-$NGINX_VERSION.tar.gz
tar -zxvf ./nginx-$NGINX_VERSION.tar.gz
cd nginx-$NGINX_VERSION
# 下载 PCRE 源码
echo "正在下载 PCRE 源码..."
wget https://mirrors.oneinstack.com/oneinstack/src/pcre-$PCRE_VERSION.tar.gz
tar -zxvf ./pcre-$PCRE_VERSION.tar.gz
# 下载 openssl 源码
echo "正在下载 OpenSSL 源码..."
wget https://mirrors.oneinstack.com/oneinstack/src/openssl-1.1.1w.tar.gz
tar -zxvf ./openssl-1.1.1w.tar.gz

# close debug
sed -i 's@CFLAGS="$CFLAGS -g"@#CFLAGS="$CFLAGS -g"@' auto/cc/gcc

# 编译和安装 Nginx
echo "正在编译 Nginx..."
./configure --prefix=${nginx_install_dir} --user=${run_user} --group=${run_group} --with-http_stub_status_module --with-http_sub_module --with-http_v2_module --with-http_ssl_module --with-stream --with-stream_ssl_preread_module --with-stream_ssl_module --with-http_gzip_static_module --with-http_realip_module --with-http_flv_module --with-http_mp4_module --with-openssl=./openssl-${OPENSSL_VERSION} --with-pcre=./pcre-${PCRE_VERSION} --with-pcre-jit
make -j ${THREAD} && make install

if [ -e "${nginx_install_dir}/conf/nginx.conf" ]; then
	popd > /dev/null
  #rm -rf pcre-${PCRE_VERSION}* openssl-${OPENSSL_VERSION}* nginx-${NGINX_VERSION}* ${nginx_install_dir}*
  echo "${CSUCCESS}Nginx installed successfully! ${CEND}"
else
    rm -rf pcre-${PCRE_VERSION}* openssl-${OPENSSL_VERSION}* nginx-${NGINX_VERSION}* ${nginx_install_dir}*
    echo "${CFAILURE}Nginx install failed, Please Contact the author! ${CEND}"
    kill -9 $$; exit 1;
fi

# 创建 Nginx 启动文件
cat > /etc/systemd/system/nginx.service <<EOF
[Unit]
Description=Nginx - high performance web server
Documentation=http://nginx.org/en/docs/
After=network.target

[Service]
Type=forking
PIDFile=/var/run/nginx.pid
ExecStartPost=/bin/sleep 0.1
ExecStartPre=/usr/local/nginx/sbin/nginx -t -c /usr/local/nginx/conf/nginx.conf
ExecStart=/usr/local/nginx/sbin/nginx -c /usr/local/nginx/conf/nginx.conf
ExecReload=/bin/kill -s HUP $MAINPID
ExecStop=/bin/kill -s QUIT $MAINPID
TimeoutStartSec=120
LimitNOFILE=1000000
LimitNPROC=1000000
LimitCORE=1000000

[Install]
WantedBy=multi-user.target

EOF

# 创建 Nginx Proxy 配置文件
cat > ${nginx_install_dir}/conf/proxy.conf << EOF
proxy_connect_timeout 300s;
proxy_send_timeout 900;
proxy_read_timeout 900;
proxy_buffer_size 32k;
proxy_buffers 4 64k;
proxy_busy_buffers_size 128k;
proxy_redirect off;
proxy_hide_header Vary;
proxy_set_header Accept-Encoding '';
proxy_set_header Referer \$http_referer;
proxy_set_header Cookie \$http_cookie;
proxy_set_header Host \$host;
proxy_set_header X-Real-IP \$remote_addr;
proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
proxy_set_header X-Forwarded-Proto \$scheme;
EOF

# 配置默认的 nginx.conf
echo "正在创建 nginx 配置文件..."
cat > ${nginx_install_dir}/conf/nginx.conf << 'EOF'
user www www;
worker_processes auto;

error_log /data/wwwlogs/error_nginx.log crit;
pid /var/run/nginx.pid;
worker_rlimit_nofile 51200;

events {
  use epoll;
  worker_connections 51200;
  multi_accept on;
}

http {
  include mime.types;
  default_type application/octet-stream;
  server_names_hash_bucket_size 128;
  client_header_buffer_size 32k;
  large_client_header_buffers 4 32k;
  client_max_body_size 1024m;
  client_body_buffer_size 10m;
  sendfile on;
  tcp_nopush on;
  keepalive_timeout 120;
  server_tokens off;
  tcp_nodelay on;

  fastcgi_connect_timeout 300;
  fastcgi_send_timeout 300;
  fastcgi_read_timeout 300;
  fastcgi_buffer_size 64k;
  fastcgi_buffers 4 64k;
  fastcgi_busy_buffers_size 128k;
  fastcgi_temp_file_write_size 128k;
  fastcgi_intercept_errors on;

  #Gzip Compression
  gzip on;
  gzip_buffers 16 8k;
  gzip_comp_level 6;
  gzip_http_version 1.1;
  gzip_min_length 256;
  gzip_proxied any;
  gzip_vary on;
  gzip_types
    text/xml application/xml application/atom+xml application/rss+xml application/xhtml+xml image/svg+xml
    text/javascript application/javascript application/x-javascript
    text/x-json application/json application/x-web-app-manifest+json
    text/css text/plain text/x-component
    font/opentype application/x-font-ttf application/vnd.ms-fontobject
    image/x-icon;
  gzip_disable "MSIE [1-6]\.(?!.*SV1)";

  ##Brotli Compression
  #brotli on;
  #brotli_comp_level 6;
  #brotli_types text/plain text/css application/json application/x-javascript text/xml application/xml application/xml+rss text/javascript application/javascript image/svg+xml;

  ##If you have a lot of static files to serve through Nginx then caching of the files' metadata (not the actual files' contents) can save some latency.
  #open_file_cache max=1000 inactive=20s;
  #open_file_cache_valid 30s;
  #open_file_cache_min_uses 2;
  #open_file_cache_errors on;

  log_format json escape=json '{"@timestamp":"$time_iso8601",'
                      '"server_addr":"$server_addr",'
                      '"remote_addr":"$remote_addr",'
                      '"scheme":"$scheme",'
                      '"request_method":"$request_method",'
                      '"request_uri": "$request_uri",'
                      '"request_length": "$request_length",'
                      '"uri": "$uri", '
                      '"request_time":$request_time,'
                      '"body_bytes_sent":$body_bytes_sent,'
                      '"bytes_sent":$bytes_sent,'
                      '"status":"$status",'
                      '"upstream_time":"$upstream_response_time",'
                      '"upstream_host":"$upstream_addr",'
                      '"upstream_status":"$upstream_status",'
                      '"host":"$host",'
                      '"http_referer":"$http_referer",'
                      '"http_user_agent":"$http_user_agent"'
                      '}';

######################## default ############################
  server {
    listen 80;
    server_name _;
    access_log /data/wwwlogs/access_nginx.log combined;
    root /data/wwwroot/default;
    index index.html index.htm index.php;
    #error_page 404 /404.html;
    #error_page 502 /502.html;
    location /nginx_status {
      stub_status on;
      access_log off;
      allow 127.0.0.1;
      deny all;
    }
    location ~ [^/]\.php(/|$) {
      #fastcgi_pass remote_php_ip:9000;
      fastcgi_pass unix:/dev/shm/php-cgi.sock;
      fastcgi_index index.php;
      include fastcgi.conf;
    }
    location ~ .*\.(gif|jpg|jpeg|png|bmp|swf|flv|mp4|ico)$ {
      expires 30d;
      access_log off;
    }
    location ~ .*\.(js|css)?$ {
      expires 7d;
      access_log off;
    }
    location ~ ^/(\.user.ini|\.ht|\.git|\.svn|\.project|LICENSE|README.md) {
      deny all;
    }
    location /.well-known {
      allow all;
    }
  }
########################## vhost #############################
  include vhost/*.conf;
}
EOF

# 启动 Nginx 服务
echo "启动 Nginx 服务..."
nginx

# 配置 nginx 环境变量
echo "正在将 nginx 添加到环境变量中..."
ln -sf /usr/local/nginx/sbin/nginx /usr/bin/nginx

# 输出安装信息
echo "Nginx $NGINX_VERSION 安装完成！"
echo "默认配置文件位于 /usr/local/nginx/conf/nginx.conf"