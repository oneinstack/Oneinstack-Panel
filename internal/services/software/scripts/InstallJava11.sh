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