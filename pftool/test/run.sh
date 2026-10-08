#!/bin/bash
# Integration test: a real Postfix queue in an Alpine container and a MariaDB
# container with mail_sender_policy rows. Needs docker and python3 on the
# host and TCP port 12525 free for the SMTP sink that receives the alert.
set -u
E=$(cd "$(dirname "$0")" && pwd)
case "$(uname -m)" in
  x86_64)  BIN=$E/../dist/pftool_x86_64;  TARGET=build-amd64 ;;
  aarch64) BIN=$E/../dist/pftool_aarch64; TARGET=build-arm64 ;;
  *) echo "unsupported host arch $(uname -m)"; exit 1 ;;
esac
(cd "$E/.." && make -s "$TARGET") || exit 1
OUT=$(mktemp -d)
FAIL=0

cleanup() {
  docker rm -f pf-postfix pf-mariadb >/dev/null 2>&1
  docker network rm pf-e2e >/dev/null 2>&1
  [ -f "$OUT/sink.pid" ] && kill "$(cat "$OUT/sink.pid")" 2>/dev/null
}
trap 'cleanup; rm -rf "$OUT"' EXIT
cleanup

python3 "$E/smtp_sink.py" 12525 "$OUT/sink" > "$OUT/sink.log" 2>&1 & echo $! > "$OUT/sink.pid"
docker network create pf-e2e >/dev/null
docker run -d --name pf-mariadb --network pf-e2e -e MARIADB_ROOT_PASSWORD=root \
  -v "$E/schema.sql:/docker-entrypoint-initdb.d/schema.sql:ro" mariadb:11 >/dev/null
docker run -d --name pf-postfix --network pf-e2e --add-host=host.docker.internal:host-gateway \
  -v "$BIN:/usr/local/bin/pftool:ro" alpine:latest sh -c '
    apk add -q postfix >/dev/null 2>&1
    for n in pfdel pfhold pfunhold find_hold; do ln -s /usr/local/bin/pftool /usr/local/bin/$n; done
    postconf -e inet_interfaces=loopback-only inet_protocols=ipv4 myhostname=pf.test.example mydestination= \
      defer_transports=smtp alias_maps= alias_database= maillog_file=/var/log/postfix.log compatibility_level=3.6
    postfix start >/dev/null 2>&1
    sleep infinity' >/dev/null

echo "== waiting for mariadb and postfix"
for i in $(seq 1 90); do
  docker exec pf-mariadb mariadb -uroot -proot -e 'select 1 from ysmaster.mail_sender_policy limit 1' >/dev/null 2>&1 && break; sleep 2
done
for i in $(seq 1 30); do
  docker exec pf-postfix postfix status >/dev/null 2>&1 && break; sleep 2
done
docker exec pf-postfix postfix status 2>&1 | tail -1

inject() { # sender rcpt subject
  docker exec -i pf-postfix sendmail -f "$1" -- "$2" <<MSG
Received: from laptop (unknown [203.0.113.5])
	(Authenticated sender: $1)
	by pf.test.example (Postfix) with ESMTPSA
From: $1
To: $2
Subject: $3

body of $3
MSG
}
inject alice@spam.example bob@remote.example "alice one"
inject alice@spam.example bob@remote.example "alice two"
inject dave@other.example bob@remote.example "dave one"
inject erin@erin.example bob@remote.example "erin one"
inject erin@erin.example bob@remote.example "erin two"
inject erin@erin.example bob@remote.example "erin three"
inject stale@old.example bob@remote.example "stale one"
inject nobody@none.example bob@remote.example "nobody one"
sleep 3

q() { docker exec pf-postfix postqueue -j | python3 "$E/showq.py"; }
check() { # description expected actual
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want $2, got $3"; FAIL=1; fi
}
count() { # queue-name-or-empty sender-or-empty
  q | awk -v qn="$1" -v s="$2" '(qn == "" || $1 == qn) && (s == "" || $3 == s)' | wc -l
}

echo "== initial queue"; q
check "8 deferred messages" 8 "$(count deferred "")"

docker exec pf-postfix pfhold dave@other.example 2>/dev/null
docker exec pf-postfix pfhold erin.example 2>/dev/null
docker exec pf-postfix pfunhold erin.example 1 2>/dev/null
echo "== queue after pfhold dave, pfhold erin.example, pfunhold erin.example 1"; q
check "dave held" 1 "$(count hold dave@other.example)"
check "two erin held after releasing one" 2 "$(count hold erin@erin.example)"
docker exec pf-postfix pfunhold erin.example lots >/dev/null 2>&1; check "pfunhold rejects a bad max" 1 $?
docker exec pf-postfix pfdel >/dev/null 2>&1; check "pfdel without an argument is a usage error" 2 $?

FH="docker exec -e DBHOST=pf-mariadb -e DBNAME=ysmaster -e DBUSER=root -e DBPASS=root -e MAIL_SERVER=host.docker.internal:12525 -e ALERT_FROM=alert@test.example -e ALERT_TO=admin@test.example -e PORTAL_URL=https://portal.example/queue -e RELEASE_MAX=1 -e FIND_HOLD_CONFIG=/nonexistent pf-postfix find_hold"
echo "== find_hold run 1 (RELEASE_MAX=1)"; $FH 2>/dev/null; check "find_hold exit status" 0 $?
sleep 1
echo "== queue after run 1"; q
check "alice held by HOLD row" 2 "$(count hold alice@spam.example)"
check "dave deleted (status 2)" 0 "$(count "" dave@other.example)"
check "one erin released (status 3, RELEASE_MAX=1)" 1 "$(count hold erin@erin.example)"
check "stale status 1 row older than 45 min not re-held" 1 "$(count deferred stale@old.example)"
ROW=$(docker exec pf-mariadb mariadb -uroot -proot -N -e "select status, length(hash)>0 from ysmaster.mail_sender_policy where sender='alice@spam.example'" 2>/dev/null | tr '\t' ' ')
check "alice row marked status 1 with a hash" "1 1" "$ROW"
check "one alert mail received" 1 "$(ls "$OUT/sink" 2>/dev/null | wc -l)"
ALERT=$(cat "$OUT/sink"/*.eml 2>/dev/null)
case "$ALERT" in
  *"Subject: Spam: 2, alice@spam.example, alice@spam.example"*"/queue/"*"/accept"*"Content-Type: message/rfc822"*) echo "ok   alert has subject, portal links and the rfc822 part" ;;
  *) echo "FAIL alert content"; echo "$ALERT" | head -40; FAIL=1 ;;
esac

echo "== find_hold run 2"; $FH 2>/dev/null
sleep 1
check "no second alert for a status 1 row" 1 "$(ls "$OUT/sink" | wc -l)"
check "last erin released on the second run" 0 "$(count hold erin@erin.example)"
check "nobody present before pfdel" 1 "$(count "" nobody@none.example)"
docker exec pf-postfix pfdel none.example >/dev/null 2>&1
check "pfdel by domain fragment" 0 "$(count "" nobody@none.example)"
echo "== final queue"; q

[ $FAIL -eq 0 ] && echo "== PASS" || { echo "== FAIL"; exit 1; }
