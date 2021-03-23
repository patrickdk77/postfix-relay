# bump: debian-buster-slim /FROM debian:(.*)/ docker:debian|/^buster-.*-slim/|sort
FROM alpine
RUN \
  apk add --no-cache procps postfix libsasl opendkim opendkim-utils \
      ca-certificates rsyslog bash

COPY rootfs/ /

# Default config:
# Open relay, trust docker links for firewalling.
# Try to use TLS when sending to other smtp servers.
# No TLS for connecting clients, trust docker network to be safe
ENV \
  POSTFIX_myhostname=hostname \
  POSTFIX_mydestination=localhost \
  POSTFIX_mynetworks=0.0.0.0/0 \
  POSTFIX_smtp_tls_security_level=may \
  POSTFIX_smtpd_tls_security_level=none \
  OPENDKIM_Socket=inet:12301@localhost \
  OPENDKIM_Mode=sv \
  OPENDKIM_UMask=002 \
  OPENDKIM_Syslog=yes \
  OPENDKIM_InternalHosts="0.0.0.0/0, ::/0" \
  OPENDKIM_KeyTable=refile:/etc/opendkim/KeyTable \
  OPENDKIM_SigningTable=refile:/etc/opendkim/SigningTable
#VOLUME ["/var/lib/postfix", "/var/mail", "/var/spool/postfix", "/etc/opendkim/keys"]
EXPOSE 25 587
CMD ["/docker-run"]
