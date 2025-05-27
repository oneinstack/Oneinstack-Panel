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