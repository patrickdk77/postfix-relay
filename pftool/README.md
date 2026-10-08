# pftool

One binary that replaces the pfdel, pfhold, pfunhold and find_hold Perl
scripts for a Postfix instance with a single queue. Symlink it under each
name, or call `pftool <name> ...`. All four need root, because postsuper and
postcat do.

```
pfdel <address|domain>            delete queued mail from or to the address
pfhold <address|domain>           put queued mail from or to the address on hold
pfunhold <address|domain> [max]   release held mail, at most max messages
find_hold                         act on mail_sender_policy HOLD decisions
```

The argument is a case-insensitive substring of the envelope sender or of
any recipient address, as in the old scripts, because sender addresses are
often random. `spam.example` matches `alice@spam.example`,
`frank@sub.spam.example` and `x@spam.example.community`, and `@spam.` matches
them too. It is a plain substring, not a regular expression. This program
reads `postqueue -j` instead of `postqueue -p`, so the substring is compared
with addresses only, never with a queue ID or a delay reason.

## find_hold

Each run does what the cron job on the old server did, for the one local
queue:

1. Puts queued mail on hold for every sender whose `mail_sender_policy` row
   has `policy='HOLD'` and `status=0`, or `status=1` with `lastupdate` inside
   the last `HOLD_RECHECK_MINUTES`.
2. Groups the held mail by sender and looks each sender up in
   `mail_sender_policy`, falling back to a row for the sender's domain.
3. `status=0`: mails the envelope summary and one held message to `ALERT_TO`
   with the portal accept and delete links, then sets `status=1`, `hash` and
   `lastupdate`. A row without a hash gets a new 32 character one.
   `status=2`: deletes all queued mail for the row's sender or domain.
   `status=3`: releases up to `RELEASE_MAX` held messages for it.

It prints one `count<TAB>sender` line per held sender, as before. A message
that is held in step 1 is reported and alerted in the same run. Two copies
cannot run at once; the second exits while `LOCK_FILE` is held.

Configuration comes from the environment, with `/etc/find_hold.conf` (or the
file named by `FIND_HOLD_CONFIG`) read first in `KEY=value` form. Variables
already set in the environment win.

| Variable | Default | Meaning |
|---|---|---|
| `DBHOST`, `DBPORT`, `DBNAME`, `DBUSER`, `DBPASS` | port 3306 | MySQL with the `mail_sender_policy` table. `DBHOST` may carry its own `:port`. |
| `MAIL_SERVER` | `localhost:25` | SMTP server for the alert mail, no authentication. |
| `ALERT_FROM`, `ALERT_TO` | none | Alert sender and recipient. Required unless `SEND_EMAIL=false`. |
| `PORTAL_URL` | `https://example.com/queue` | Prefix for the `/<hash>/accept` and `/<hash>/delete/<account>` links. |
| `RELEASE_MAX` | `75` | Held messages released per run for a `status=3` row. |
| `HOLD_RECHECK_MINUTES` | `45` | How long a `status=1` row keeps holding new mail. |
| `SEND_EMAIL` | `true` | `false` skips the alert and leaves the row at `status=0`. |
| `LOCK_FILE` | `/run/find_hold.lock` | flock file. |
| `FIND_HOLD_INTERVAL` | unset | Set to a duration such as `60s` to loop inside one process instead of using cron. |

## Why find_hold still exists

Postfix already reads `mail_sender_policy` at SMTP time. postfix-user applies
`check_sasl_access` and `check_sender_access` against it (postfix-bulk the
second one), so a `HOLD` row holds new mail from that login or sender with no
script involved, and the `REJECT ...` or `dunno` value the portal writes
later is applied the same way. A table cannot do the rest: move mail that was
accepted before the row existed into the hold queue, release or delete held
mail, or send the admin a sample. Only postsuper and postcat do those, and
that is all find_hold does.

The rows are written by whatever detects a sender going over the limit. That
code is not in the newmail tree. If it can return `HOLD` as the policy
action in the same step as inserting the row, new mail stops at once and
find_hold only has to sweep the burst already in the queue.

## Deploy

The binary has to run where the queue is, as root, with that instance's
Postfix configuration. In Kubernetes that means the postfix-user and
postfix-bulk pods, the two that apply the sender policy map.

1. `make` in the postfix-relay root builds and pushes the image. The
   Dockerfile compiles this module in a `golang:alpine` stage and installs
   `/usr/local/bin/pftool` with `pfdel`, `pfhold`, `pfunhold` and `find_hold`
   linked to it.
2. Write `/etc/find_hold.conf` and mount it from a Secret, since it carries
   the database password. The values the old server used:

       DBHOST=db.default.svc.cluster.local.
       DBPORT=3306
       DBNAME=dbname
       DBUSER=postfix
       DBPASS=...
       MAIL_SERVER=smtp.example.com:25
       ALERT_FROM=alert@example.com
       ALERT_TO=sysadmin.alert@example.com
       PORTAL_URL=https://example.com/queue
       FIND_HOLD_INTERVAL=60s

   The old script used a MySQL user with write access to
   `mail_sender_policy`; `postfix` only needs `UPDATE` on that table in
   addition to its reads.
3. Run `find_hold` as a second container in each of those StatefulSets,
   from the same postfix-relay image, as root, with the `spool` volume and
   the main.cf and master.cf mounts the postfix container has. With
   `FIND_HOLD_INTERVAL` set it loops in one process, so no cron is needed.
   Without it, run it from cron inside the postfix container instead.

The old server ran one cron job over four queues. Here every pod has one
queue, so every pod runs its own copy against its own spool.

By hand from inside a pod:

    kubectl exec postfix-user-0 -c postfix -- pfhold spam.example
    kubectl exec postfix-user-0 -c postfix -- pfunhold spam.example 50
    kubectl exec postfix-user-0 -c postfix -- pfdel xk3j2q@

## Build and test

`make` writes `dist/pftool_x86_64` and `dist/pftool_aarch64` for a glibc
host. They do not run on Alpine, so the postfix-relay image compiles the
module itself. `make test` runs vet and the unit tests.

`test/run.sh` is the integration test. It builds the binary for the host
architecture, starts Alpine Postfix and MariaDB containers, queues eight
messages, exercises pfhold, pfunhold and pfdel, then runs find_hold twice
against rows in each status and checks the queue, the table and the alert
mail. It needs docker and python3 and port 12525 on the host for the SMTP
sink that receives the alert. It prints one `ok` or `FAIL` line per check
and exits non-zero on any failure.

    test/run.sh
