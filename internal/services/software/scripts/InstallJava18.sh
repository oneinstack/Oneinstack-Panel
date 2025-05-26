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