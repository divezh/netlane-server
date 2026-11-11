ARG UBUNTU_VERSION=24.04
FROM ubuntu:${UBUNTU_VERSION}
ENV DEBIAN_FRONTEND=noninteractive container=docker
RUN apt-get update -qq && apt-get install -y --no-install-recommends \
    systemd systemd-sysv dbus ca-certificates certbot curl openssl python3 python3-systemd \
    iproute2 fail2ban ufw openssh-server util-linux kmod && \
    rm -rf /var/lib/apt/lists/* && mkdir -p /run/sshd
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
