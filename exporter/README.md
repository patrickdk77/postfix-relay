# Postfix exporter

Prometheus exporter for [Postfix](http://www.postfix.org/). It reads queue
contents from Postfix's showq socket and counts events from Postfix's log
lines.

This is a fork of [Hsn723/postfix_exporter](https://github.com/Hsn723/postfix_exporter)
0.21.2, which is itself a fork of
[kumina/postfix_exporter](https://github.com/kumina/postfix_exporter). The
reply parsing and the config file come from
[sergeymakinen/postfix_exporter](https://github.com/sergeymakinen/postfix_exporter)
2.2.1.

## Where the data comes from

Queue metrics come from the showq socket, `/var/spool/postfix/public/showq`
by default. They work without a log source. Start the exporter without
`--postfix.logfile_path` and it serves queue metrics only.

Event metrics come from Postfix's log lines, read either from standard input
or from a file:

- `--postfix.logfile_path=-` (or `/dev/stdin`) reads standard input. The
  exporter exits when standard input closes.
- Any other path is tailed as a file, and rotation is handled. A missing
  file stops the exporter at startup. With `--no-postfix.logfile_must_exist`,
  a missing file means queue metrics only, the same as leaving the path out.
  A file created later is not picked up.

The systemd journal, Docker and Kubernetes log sources from upstream are
removed.

## Running under rsyslog

rsyslog's `omprog` module starts the exporter and writes every mail log line
to its standard input. On Alpine the module is in the `rsyslog-prog`
package. Put the action before any action that ends with `stop`:

```
module(load="omprog")
if $syslogfacility-text == "mail" then {
  action(type="omprog"
         binary="/usr/local/bin/postfix_exporter --postfix.instance=auto --postfix.logfile_path=- --web.listen-address=:9154"
         template="RSYSLOG_TraditionalFileFormat"
         queue.type="LinkedList"
         queue.size="10000")
}
```

The action's own queue keeps a slow exporter from holding up other rsyslog
actions. omprog discards the exporter's output unless `output=` names a file.
When rsyslog stops, the pipe closes and the exporter exits. If the exporter
dies, omprog starts it again.

## Instances and the subprogram label

Postfix tags each log line with its `syslog_name`, a slash and the daemon,
for example `mail_in/smtpd[123]`. The exporter parses only lines whose tag
starts with a name given to `--postfix.instance`. Lines from anything else
count in `postfix_foreign_log_entries_total`.

`--postfix.instance` is repeatable and takes three forms:

| Value | Meaning |
|---|---|
| `postfix` | A syslog name. This is the default. |
| `relay=postfix/relay` | A syslog name set by a master.cf service with `-o syslog_name`. Its lines report under that service's name. |
| `auto` | Runs `postconf -h syslog_name` and `postconf -xP`, and adds main.cf's syslog name plus every master.cf `-o syslog_name` override. |

The `subprogram` label is the service and daemon, in the form Postfix uses
for `postfix/submission/smtpd`. With `auto` on a master.cf like this:

```
relay      unix  -  -  n  -  -  smtp  -o syslog_name=postfix/$service_name
yahoo      unix  -  -  n  -  -  smtp  -o syslog_name=mail_batch-yahoo
submission inet  n  -  n  -  -  smtpd -o syslog_name=mail_user/submission
```

lines report as `relay/smtp`, `yahoo/smtp` and `submission/smtpd`. When
several names match a line, the longest wins.

Postfix logs nothing else that tells services apart. A service without its
own `syslog_name` logs under the main name, so submission without the
override above looks the same as port 25.

## Options

Turn off a true/false flag with its `--no-` form, such as
`--no-postfix.logfile_must_exist`. The flag parser rejects `=false`.

| Flag | Default | Description |
|---|---|---|
| `--postfix.instance` | `postfix` | Syslog names to parse, as described above. Repeatable. |
| `--postfix.postconf_path` | `postconf` | postconf binary used by `--postfix.instance=auto`. |
| `--postfix.logfile_path` | empty | Log file to tail, `-` for standard input, or empty for queue metrics only. |
| `--postfix.logfile_must_exist` | `true` | Fail at startup if the log file is missing. `--no-postfix.logfile_must_exist` runs on queue metrics only instead. |
| `--postfix.logfile_poll` | `false` | Poll the log file instead of using inotify. |
| `--postfix.logfile_debug` | `false` | Debug logging for the file tailer. |
| `--postfix.showq_path` | `/var/spool/postfix/public/showq` | showq socket. |
| `--postfix.showq_network` | `unix` | `unix`, or `tcp` for a showq service exposed over TCP. |
| `--postfix.showq_port` | `10025` | showq port when the network is TCP. |
| `--postfix.queue_directory` | `/var/spool/postfix` | Where the corrupt queue is counted. Empty turns that count off. |
| `--config.file` | empty | Reply text rules, described below. |
| `--postfix.<type>_service_label` | the type's name | Extra names routed to a daemon's parser, for cleanup, lmtp, pipe, qmgr, smtp, smtpd, bounce, virtual and postscreen. Repeatable. The last part of the tag already routes `relay/smtp` to smtp, so these are rarely needed. |
| `--log.unsupported` | `false` | Log every line no parser understood. |
| `--watchdog` | `false` | Reopen the log file when the tailer stops. |
| `--web.listen-address` | `:9154` | Listen address. |
| `--web.telemetry-path` | `/metrics` | Metrics path. |
| `--web.config.file` | empty | TLS and basic auth, see the [exporter-toolkit docs](https://github.com/prometheus/exporter-toolkit/blob/master/docs/web-configuration.md). |
| `--log.level`, `--log.format` | `info`, `logfmt` | The exporter's own logging. |

## Reply text rules

Reply metrics carry a `text` label. It is empty unless a rule in the
`--config.file` YAML matches. Free reply text would create a new series for
every address and queue ID, so rules map it to a small set of values. Each
list is tried in order and the first match wins. `match` picks what the
regexp runs on: `text` (the default), `code` or `enhanced_code`. `text` can
use `$1` style references to the regexp's groups.

```yaml
reject_replies:      # postfix_access_actions_total
  - regexp: (Relay access denied|blocked using [a-z0-9.-]+)
    text: $1
  - regexp: ^4
    match: code
    text: temporary
status_replies:      # postfix_delivery_status_replies_total
  - statuses: [deferred]
    regexp: (?i)rate|too many|try again later
    text: rate_limited
smtp_replies:        # postfix_smtp_replies_total
  - regexp: (?i)gr[ae]ylist
    text: greylisted
```

`status_replies` rules also take `statuses` and `not_statuses` lists. An
unknown key, a bad regexp, a missing `regexp` or an empty `text` stops the
exporter at startup.

## Metrics

All names start with `postfix_`. Labels added in this fork are listed with
each metric.

### Queues, from showq

| Metric | Labels | Meaning |
|---|---|---|
| `showq_queue_depth` | queue | Messages in maildrop, incoming, active, deferred and hold. |
| `showq_queue_recipients` | queue | Recipients of those messages. New. |
| `showq_delayed_recipients` | queue, code, enhanced_code | Recipients with a deferral reason, by the remote reply code in it. The codes are empty for reasons without a reply, such as a connect timeout. New. |
| `showq_forced_expire_messages` | queue | Messages marked with `postsuper -e`. New. |
| `showq_message_size_bytes` | queue | Histogram of message sizes. |
| `showq_message_age_seconds` | queue | Histogram of message ages. |
| `queue_corrupt_messages` | | Files in the corrupt queue, which showq never reads. New. |
| `up` | path | 1 when showq and the log source are working. |

### Mail entering Postfix

| Metric | Labels | Meaning |
|---|---|---|
| `messages_received_total` | subprogram | Each new message: `client=` from smtpd and qmqpd, `uid=` from pickup for mail sent with sendmail or postdrop. New. |
| `access_actions_total` | subprogram, action, command, code, enhanced_code, text | Every reject, reject_warning, discard, hold, filter, redirect and milter action, at every SMTP command, from smtpd, postscreen and cleanup. New. |
| `smtpd_connects_total`, `smtpd_disconnects_total` | subprogram | Connections. Label new. |
| `smtpd_connections_lost_total` | subprogram, after_stage | Lost connections. Label new. |
| `smtpd_messages_processed_total` | subprogram, sasl_method | Messages accepted. Label new. |
| `smtpd_messages_rejected_total` | subprogram, code | RCPT rejects only. Kept from upstream. Label new. |
| `smtpd_sasl_authentication_failures_total` | subprogram, method | Failed logins. Labels new. |
| `smtpd_tls_connections_total` | subprogram, trust, protocol, cipher, secret_bits, algorithm_bits | Incoming TLS. Label new. |
| `smtpd_forward_confirmed_reverse_dns_errors_total` | subprogram | Clients whose hostname does not resolve back. Label new. |
| `postscreen_*` | | Unchanged from upstream. |
| `cleanup_actions_total` | action, part | header_checks and body_checks actions. New. |
| `cleanup_messages_processed_total`, `cleanup_messages_rejected_total` | | Unchanged. |

### Delivery

| Metric | Labels | Meaning |
|---|---|---|
| `delivery_statuses_total` | subprogram, status | Every delivery attempt by smtp, lmtp, local, virtual, pipe, error and the rest, by status. New. |
| `delivery_delay_seconds` | subprogram, status | Histogram of time from arrival to the attempt. New. |
| `delivery_stage_delay_seconds` | subprogram, stage | Histogram of the four `delays=` stages for every agent. New. |
| `delivery_status_replies_total` | subprogram, status, code, enhanced_code, text | Delivery attempts by the reply received. New. |
| `smtp_replies_total` | code, enhanced_code, text | Remote replies logged without a status. New. |
| `bounce_notifications_total` | subprogram, recipient, type | Non-delivery, delay and delivery status notices to the sender or postmaster. New. |
| `smtp_messages_processed_total` | subprogram, status | smtp attempts. Label new. |
| `smtp_delivery_delay_seconds` | subprogram, stage | Label new. Upstream recorded every stage as `stage=""`, which is fixed. |
| `smtp_deferred_messages_by_dsn_total`, `smtp_bounced_messages_by_dsn_total` | subprogram, dsn | Label new. |
| `smtp_connection_timed_out_total`, `smtp_connection_refused_total` | subprogram | Label new. Timeouts now also match musl's "Operation timed out". |
| `smtp_tls_connections_total` | subprogram, trust, protocol, cipher, secret_bits, algorithm_bits | Label new. |
| `lmtp_delivery_delay_seconds` | subprogram, stage | Label new. |
| `pipe_delivery_delay_seconds` | subprogram, relay, stage | Label new. |
| `virtual_delivered_total`, `bounce_*_notification_total`, `qmgr_messages_*` | | Unchanged. |
| `qmgr_statuses_total` | status | Queue manager status changes, such as expired. New. |

### Postfix itself

| Metric | Labels | Meaning |
|---|---|---|
| `master_process_failures_total` | program, reason | Daemons that exited with an error or were killed. A daemon dying on bad configuration shows up here. New. |
| `master_throttles_total` | program | Services master throttled after startup failures. New. |
| `master_events_total` | event | Starts, reloads and stops. New. |
| `log_entries_total` | subprogram, severity | Every parsed line. `severity="fatal"` is worth an alert. New. |
| `foreign_log_entries_total` | | Lines from other programs. New. |
| `unsupported_log_entries_total` | service, level | Lines no parser understood. Lines from other programs no longer count here. |

## Building

```sh
make test     # go test -race
make build    # static, stripped dist/postfix_exporter_x86_64 and _aarch64
make update   # update dependencies
```

The builds use `CGO_ENABLED=0`, so they run on Alpine.

## License

Apache License 2.0, in `LICENSE`. The files that came from sergeymakinen's
exporter (`config/config.go` and `exporter/reply.go`) are also under the BSD
3-Clause License in `LICENSE.sergeymakinen`.
