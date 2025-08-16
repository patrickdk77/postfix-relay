# bump: debian-buster-slim /FROM debian:(.*)/ docker:debian|/^buster-.*-slim/|sort
FROM alpine
RUN \
 apk add --no-cache procps postfix postfix-mysql postfix-pcre libsasl opendkim opendkim-utils postsrsd \
      ca-certificates rsyslog bash \
 && apk add --no-cache perl-email-simple perl-io-multiplex perl-dbd-mysql perl-net-dns perl-mime-lite \
 	perl-sys-syslog perl-mail-dkim perl-net-smtp-ssl perl-net-server perl-net-ip perl-email-mime \
 	perl-email-address perl-capture-tiny perl-moo perl-moox-types-mooselike perl-sub-exporter perl-try-tiny \
 	perl-mro-compat perl-module-pluggable perl-module-runtime perl-devel-stacktrace perl-time-hires \
 && mkdir -p /var/spool/rsyslog \
 && mkdir -p /etc/opendkim/keys \
 && mkdir -p /run \
 && printf '\n\
slow      unix  -       -       n       -       -       smtp\n\
  -o syslog_name=postfix/slow/smtp\n\
\n\
' >> /etc/postfix/master.cf \
 && rm /etc/rsyslog.conf

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
  POSTFIX_append_dot_mydomain=no \
  POSTFIX_bounce_queue_lifetime=1d \
  POSTFIX_delay_warning_time=0h \
  POSTFIX_disable_vrfy_command=yes \
  POSTFIX_smtp_header_checks="regexp:/etc/postfix/header/trim.regexp" \
  POSTFIX_enable_long_queue_ids=yes \
  POSTFIX_mua_helo_restrictions="" \
  POSTFIX_mua_client_restrictions="" \
  POSTFIX_mua_sender_restrictions="" \
  POSTFIX_mua_relay_restrictions="\${{\$compatibility_level} <level {1} ? {} : {permit_mynetworks, permit_sasl_authenticated, defer_unauth_destination}}" \
  POSTFIX_mua_recipient_restrictions="" \
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
