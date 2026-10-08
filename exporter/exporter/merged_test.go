package exporter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/patrickdk77/postfix_exporter/config"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestExporter(t *testing.T, opts ...ServiceLabel) *PostfixExporter {
	t.Helper()
	return NewPostfixExporter(nil, nil, false, opts...)
}

func feed(e *PostfixExporter, lines ...string) {
	for _, line := range lines {
		e.CollectFromLogLine(line)
	}
}

var batchInstances = []Instance{
	{SyslogName: "mail_batch"},
	{SyslogName: "postfix/relay", Service: "relay"},
	{SyslogName: "mail_batch-yahoo", Service: "yahoo"},
}

func TestInstanceLinesAreParsed(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t, WithInstances([]Instance{{SyslogName: "mail_user"}}))
	feed(e,
		"2026-10-04T23:35:14.126687+00:00 postfix-user-0 mail_user/smtpd[484]: connect from amavis-0.amavis-headless.default.svc.cluster.local[10.192.2.7]",
		"2026-10-04T23:35:14.126715+00:00 postfix-user-0 mail_user/smtpd[484]: disconnect from amavis-0.amavis-headless.default.svc.cluster.local[10.192.2.7] commands=0/0",
	)
	assert.Equal(t, 1.0, testutil.ToFloat64(e.smtpdConnects.WithLabelValues("smtpd")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.smtpdDisconnects.WithLabelValues("smtpd")))
	assert.Equal(t, 2.0, testutil.ToFloat64(e.logEntries.WithLabelValues("smtpd", "info")))
	assert.Equal(t, 0.0, testutil.ToFloat64(e.foreignLogEntries))
}

func TestServiceSyslogNamesMapToSubprogram(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t, WithInstances(batchInstances))
	feed(e,
		"Oct  6 12:00:00 postfix-batch-0 postfix/relay/smtp[10]: 4ABC: to=<a@example.net>, relay=mx.example.net[192.0.2.5]:25, delay=1.5, delays=0.1/0.2/0.3/0.9, dsn=2.0.0, status=sent (250 2.0.0 Ok)",
		"Oct  6 12:00:01 postfix-batch-0 mail_batch-yahoo/smtp[11]: 4ABD: to=<b@yahoo.com>, relay=mta5.am0.yahoodns.net[192.0.2.6]:25, delay=3, delays=0.1/0/1/1.9, dsn=4.7.0, status=deferred (host mta5.am0.yahoodns.net[192.0.2.6] said: 421 4.7.0 [TSS04] Messages temporarily deferred (in reply to MAIL FROM command))",
		"Oct  6 12:00:02 postfix-batch-0 mail_batch/smtp[12]: 4ABE: to=<c@example.org>, relay=mx.example.org[192.0.2.7]:25, delay=0.5, delays=0.1/0/0.2/0.2, dsn=2.0.0, status=sent (250 Ok)",
	)
	assert.Equal(t, 1.0, testutil.ToFloat64(e.deliveryStatuses.WithLabelValues("relay/smtp", "sent")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.deliveryStatuses.WithLabelValues("yahoo/smtp", "deferred")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.deliveryStatuses.WithLabelValues("smtp", "sent")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.smtpProcesses.WithLabelValues("relay/smtp", "sent")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.smtpDeferredDSN.WithLabelValues("yahoo/smtp", "4.7.0")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.deliveryStatusReplies.WithLabelValues("yahoo/smtp", "deferred", "421", "4.7.0", "")))
	assert.Equal(t, 0.0, testutil.ToFloat64(e.foreignLogEntries))
}

func TestLongestSyslogNameWins(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t, WithInstances([]Instance{{SyslogName: "postfix"}, {SyslogName: "postfix/relay", Service: "relay"}}))
	feed(e,
		"Oct  6 12:00:00 host postfix/smtpd[1]: connect from client.example.org[192.0.2.9]",
		"Oct  6 12:00:00 host postfix/relay/smtp[2]: connect to mx.example.net[192.0.2.5]:25: Connection refused",
	)
	assert.Equal(t, 1.0, testutil.ToFloat64(e.smtpdConnects.WithLabelValues("smtpd")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.smtpConnectionRefused.WithLabelValues("relay/smtp")))

	e = newTestExporter(t, WithInstances([]Instance{{SyslogName: "mail_user"}, {SyslogName: "mail_user/sub", Service: "submission"}}))
	feed(e, "Oct  6 12:00:00 host mail_user/sub/smtpd[3]: connect from client.example.org[192.0.2.9]")
	assert.Equal(t, 1.0, testutil.ToFloat64(e.smtpdConnects.WithLabelValues("submission/smtpd")))
}

func TestSubmissionAndSmtpsReportSeparately(t *testing.T) {
	t.Parallel()
	for _, instances := range [][]Instance{
		{{SyslogName: "mail_user"}},
		{{SyslogName: "mail_user"}, {SyslogName: "mail_user/submission", Service: "submission"}, {SyslogName: "mail_user/smtps", Service: "smtps"}},
	} {
		e := newTestExporter(t, WithInstances(instances))
		feed(e,
			"Oct  6 12:00:00 postfix-user-0 mail_user/smtpd[1]: 4AB1: client=a.example.org[192.0.2.1]",
			"Oct  6 12:00:00 postfix-user-0 mail_user/submission/smtpd[2]: 4AB2: client=b.example.org[192.0.2.2], sasl_method=PLAIN, sasl_username=user@example.org",
			"Oct  6 12:00:00 postfix-user-0 mail_user/smtps/smtpd[3]: 4AB3: client=c.example.org[192.0.2.3], sasl_method=LOGIN, sasl_username=user@example.org",
		)
		assert.Equal(t, 1.0, testutil.ToFloat64(e.messagesReceived.WithLabelValues("smtpd")))
		assert.Equal(t, 1.0, testutil.ToFloat64(e.messagesReceived.WithLabelValues("submission/smtpd")))
		assert.Equal(t, 1.0, testutil.ToFloat64(e.messagesReceived.WithLabelValues("smtps/smtpd")))
		assert.Equal(t, 1.0, testutil.ToFloat64(e.smtpdProcesses.WithLabelValues("submission/smtpd", "PLAIN")))
		assert.Equal(t, 1.0, testutil.ToFloat64(e.smtpdProcesses.WithLabelValues("smtps/smtpd", "LOGIN")))
	}
}

func TestAccessActionsAtEveryStage(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t)
	feed(e,
		"Oct  6 12:00:00 host postfix/smtpd[1]: NOQUEUE: reject: RCPT from client.example.org[192.0.2.9]: 554 5.7.1 <x@example.net>: Relay access denied; from=<a@example.org> to=<x@example.net> proto=ESMTP helo=<client>",
		"Oct  6 12:00:00 host postfix/smtpd[1]: NOQUEUE: reject: CONNECT from unknown[192.0.2.10]: 554 5.7.1 Service unavailable; Client host [192.0.2.10] blocked using zen.spamhaus.org; from=<> to=<> proto=SMTP",
		"Oct  6 12:00:00 host postfix/smtpd[1]: 4ABC: reject: DATA from client.example.org[192.0.2.9]: 550 5.5.3 <DATA>: Data command rejected: Multi-recipient bounce; from=<> to=<x@example.net> proto=ESMTP helo=<client>",
		"Oct  6 12:00:00 host postfix/smtpd[1]: NOQUEUE: reject_warning: RCPT from client.example.org[192.0.2.9]: 450 4.7.1 Client host rejected: cannot find your hostname; from=<a@example.org> to=<x@example.net> proto=ESMTP helo=<client>",
		"Oct  6 12:00:00 host postfix/smtpd[1]: 4ABD: milter-reject: END-OF-MESSAGE from client.example.org[192.0.2.9]: 5.7.1 Command rejected; from=<a@example.org> to=<x@example.net> proto=ESMTP helo=<client>",
		"Oct  6 12:00:00 host postfix/smtpd[1]: NOQUEUE: discard: RCPT from client.example.org[192.0.2.9]: <x@example.net>: Recipient address triggers DISCARD action; from=<a@example.org> to=<x@example.net> proto=ESMTP helo=<client>",
		"Oct  6 12:00:00 host postfix/postscreen[2]: NOQUEUE: reject: RCPT from [192.0.2.11]:4711: 550 5.7.1 Service unavailable; client [192.0.2.11] blocked using zen.spamhaus.org; from=<a@example.org>, to=<x@example.net>, proto=ESMTP, helo=<client>",
	)
	assert.Equal(t, 1.0, testutil.ToFloat64(e.accessActions.WithLabelValues("smtpd", "reject", "RCPT", "554", "5.7.1", "")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.accessActions.WithLabelValues("smtpd", "reject", "CONNECT", "554", "5.7.1", "")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.accessActions.WithLabelValues("smtpd", "reject", "DATA", "550", "5.5.3", "")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.accessActions.WithLabelValues("smtpd", "reject_warning", "RCPT", "450", "4.7.1", "")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.accessActions.WithLabelValues("smtpd", "milter-reject", "END-OF-MESSAGE", "", "5.7.1", "")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.accessActions.WithLabelValues("smtpd", "discard", "RCPT", "", "", "")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.accessActions.WithLabelValues("postscreen", "reject", "RCPT", "550", "5.7.1", "")))
	// The RCPT-only metric kept from upstream still counts its line.
	assert.Equal(t, 1.0, testutil.ToFloat64(e.smtpdRejects.WithLabelValues("smtpd", "554")))
	assert.Equal(t, 0.0, testutil.ToFloat64(e.unsupportedLogEntries.WithLabelValues("smtpd", "")))
}

func TestDeliveryStatusesForEveryAgent(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t, WithInstances([]Instance{{SyslogName: "mail_in"}}))
	feed(e,
		"Oct  6 12:00:00 postfix-incoming-0 mail_in/lmtp[1]: 4AB1: to=<u@example.org>, relay=dovecot.example.org[192.0.2.20]:24, delay=0.2, delays=0.1/0/0/0.1, dsn=2.0.0, status=sent (250 2.0.0 <u@example.org> abc Saved)",
		"Oct  6 12:00:00 postfix-incoming-0 mail_in/pipe[2]: 4AB2: to=<v@example.org>, relay=autoreply, delay=0.3, delays=0.1/0/0/0.2, dsn=2.0.0, status=sent (delivered via autoreply service)",
		"Oct  6 12:00:00 postfix-incoming-0 mail_in/error[3]: 4AB3: to=<w@example.org>, relay=none, delay=0, delays=0/0/0/0, dsn=5.0.0, status=bounced (User unknown)",
		"Oct  6 12:00:00 postfix-incoming-0 mail_in/local[4]: 4AB4: to=<root@localhost>, relay=local, delay=0.1, delays=0/0/0/0.1, dsn=2.0.0, status=sent (delivered to mailbox)",
		"Oct  6 12:00:00 postfix-incoming-0 mail_in/virtual[5]: 4AB5: to=<x@example.org>, relay=virtual, delay=0.1, delays=0/0/0/0.1, dsn=2.0.0, status=sent (delivered to maildir)",
	)
	for _, agent := range []string{"lmtp", "pipe", "local", "virtual"} {
		assert.Equal(t, 1.0, testutil.ToFloat64(e.deliveryStatuses.WithLabelValues(agent, "sent")), agent)
	}
	assert.Equal(t, 1.0, testutil.ToFloat64(e.deliveryStatuses.WithLabelValues("error", "bounced")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.deliveryStatusReplies.WithLabelValues("lmtp", "sent", "250", "2.0.0", "")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.deliveryStatusReplies.WithLabelValues("error", "bounced", "", "5.0.0", "")))
	assert.Equal(t, 20, testutil.CollectAndCount(e.deliveryStageDelays), "one series per agent and stage")
	assert.Equal(t, 1.0, testutil.ToFloat64(e.virtualDelivered))
	assert.Equal(t, 0.0, testutil.ToFloat64(e.unsupportedLogEntries.WithLabelValues("error", "")))
}

func TestSmtpStageDelaysUseStageLabels(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t)
	feed(e, "Oct  6 12:00:00 host postfix/smtp[1]: 4AB1: to=<a@example.net>, relay=mx.example.net[192.0.2.5]:25, delay=1.5, delays=0.1/0.2/0.3/0.9, dsn=2.0.0, status=sent (250 2.0.0 Ok)")
	assert.Equal(t, 4, testutil.CollectAndCount(e.smtpDelays), "upstream put every stage in one series with stage=\"\"")
}

func TestInputsAndBounceNotifications(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t, WithInstances([]Instance{{SyslogName: "mail_out"}}))
	feed(e,
		"Oct  6 12:00:00 host mail_out/pickup[1]: 4AB1: uid=0 from=<root>",
		"Oct  6 12:00:00 host mail_out/qmqpd[2]: 4AB2: client=client.example.org[192.0.2.9]",
		"Oct  6 12:00:00 host mail_out/bounce[3]: 4AB3: sender non-delivery notification: 4AB4",
		"Oct  6 12:00:00 host mail_out/bounce[3]: 4AB5: sender delay notification: 4AB6",
		"Oct  6 12:00:00 host mail_out/bounce[3]: 4AB7: postmaster non-delivery notification: 4AB8",
		"Oct  6 12:00:00 host mail_out/bounce[3]: 4AB9: sender delivery status notification: 4ABA",
	)
	assert.Equal(t, 1.0, testutil.ToFloat64(e.messagesReceived.WithLabelValues("pickup")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.messagesReceived.WithLabelValues("qmqpd")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.bounceNotifications.WithLabelValues("bounce", "sender", "non-delivery")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.bounceNotifications.WithLabelValues("bounce", "sender", "delay")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.bounceNotifications.WithLabelValues("bounce", "postmaster", "non-delivery")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.bounceNotifications.WithLabelValues("bounce", "sender", "delivery status")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.bounceNonDelivery))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.bounceDelivery))
	assert.Equal(t, 0, testutil.CollectAndCount(e.unsupportedLogEntries))
}

func TestCleanupActionsSaslQmgrAndSeverity(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t)
	feed(e,
		"Oct  6 12:00:00 host postfix/cleanup[1]: 4AB1: reject: header Subject: buy now from client.example.org[192.0.2.9]; from=<a@example.org> to=<x@example.net> proto=ESMTP helo=<client>: 5.7.1 message content rejected",
		"Oct  6 12:00:00 host postfix/cleanup[1]: 4AB2: hold: body suspicious line from client.example.org[192.0.2.9]; from=<a@example.org> to=<x@example.net> proto=ESMTP helo=<client>",
		"Oct  6 12:00:00 host postfix/cleanup[1]: 4AB3: warning: header X-Spam: yes from client.example.org[192.0.2.9]; from=<a@example.org> to=<x@example.net> proto=ESMTP helo=<client>",
		"Oct  6 12:00:00 host postfix/smtpd[2]: warning: unknown[192.0.2.30]: SASL LOGIN authentication failed: UGFzc3dvcmQ6",
		"Oct  6 12:00:00 host postfix/qmgr[3]: 4AB4: from=<a@example.org>, status=expired, returned to sender",
	)
	assert.Equal(t, 1.0, testutil.ToFloat64(e.cleanupActions.WithLabelValues("reject", "header")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.cleanupActions.WithLabelValues("hold", "body")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.cleanupActions.WithLabelValues("warning", "header")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.cleanupRejects))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.smtpdSASLAuthenticationFailures.WithLabelValues("smtpd", "LOGIN")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.qmgrStatuses.WithLabelValues("expired")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.qmgrExpires))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.logEntries.WithLabelValues("smtpd", "warning")))
	assert.Equal(t, 3.0, testutil.ToFloat64(e.logEntries.WithLabelValues("cleanup", "info")))
}

func TestConfigRulesSetTextLabels(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, `
reject_replies:
  - regexp: (Relay access denied|blocked using [a-z.]+)
    text: $1
status_replies:
  - statuses: [deferred]
    regexp: (?i)temporarily deferred
    text: rate_limited
smtp_replies:
  - regexp: ^4
    match: code
    text: temporary
`)
	e := newTestExporter(t, WithConfig(cfg))
	feed(e,
		"Oct  6 12:00:00 host postfix/smtpd[1]: NOQUEUE: reject: RCPT from client.example.org[192.0.2.9]: 554 5.7.1 <x@example.net>: Relay access denied; from=<a@example.org> to=<x@example.net> proto=ESMTP helo=<client>",
		"Oct  6 12:00:00 host postfix/smtpd[1]: NOQUEUE: reject: CONNECT from unknown[192.0.2.10]: 554 5.7.1 Service unavailable; Client host [192.0.2.10] blocked using zen.spamhaus.org; from=<> to=<> proto=SMTP",
		"Oct  6 12:00:00 host postfix/smtpd[1]: NOQUEUE: reject: RCPT from client.example.org[192.0.2.9]: 450 4.1.8 <a@bad.example>: Sender address rejected: Domain not found; from=<a@bad.example> to=<x@example.net> proto=ESMTP helo=<client>",
		"Oct  6 12:00:01 host postfix/smtp[11]: 4ABD: to=<b@yahoo.com>, relay=mta5.am0.yahoodns.net[192.0.2.6]:25, delay=3, delays=0.1/0/1/1.9, dsn=4.7.0, status=deferred (host mta5.am0.yahoodns.net[192.0.2.6] said: 421 4.7.0 [TSS04] Messages temporarily deferred (in reply to MAIL FROM command))",
		"Oct  6 12:00:02 host postfix/smtp[12]: 4ABE: host mx.example.net[192.0.2.5] said: 450 4.2.1 Mailbox busy (in reply to RCPT TO command)",
	)
	assert.Equal(t, 1.0, testutil.ToFloat64(e.accessActions.WithLabelValues("smtpd", "reject", "RCPT", "554", "5.7.1", "Relay access denied")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.accessActions.WithLabelValues("smtpd", "reject", "CONNECT", "554", "5.7.1", "blocked using zen.spamhaus.org")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.accessActions.WithLabelValues("smtpd", "reject", "RCPT", "450", "4.1.8", "")), "no rule matched")
	assert.Equal(t, 1.0, testutil.ToFloat64(e.deliveryStatusReplies.WithLabelValues("smtp", "deferred", "421", "4.7.0", "rate_limited")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.smtpReplies.WithLabelValues("450", "4.2.1", "temporary")))
}

func TestStatusRuleLimitedToStatuses(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t, `
status_replies:
  - statuses: [bounced]
    regexp: (.+)
    text: matched
`)
	e := newTestExporter(t, WithConfig(cfg))
	feed(e, "Oct  6 12:00:00 host postfix/smtp[1]: 4AB1: to=<a@example.net>, relay=mx.example.net[192.0.2.5]:25, delay=1, delays=0.1/0/0.4/0.5, dsn=2.0.0, status=sent (250 2.0.0 Ok)")
	assert.Equal(t, 1.0, testutil.ToFloat64(e.deliveryStatusReplies.WithLabelValues("smtp", "sent", "250", "2.0.0", "")))
}

func TestForeignLinesAreNotParsed(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t, WithInstances([]Instance{{SyslogName: "mail_user"}}))
	feed(e,
		"Oct  6 12:00:00 host dovecot[1]: lmtp(user@example.org): saved mail",
		"Oct  6 12:00:00 mail_user-0 rsyslogd[2]: start",
		"Oct  6 12:00:00 host postfix/smtpd[3]: connect from client.example.org[192.0.2.9]",
		"Oct  6 12:00:00 host opendkim[4]: 4AB1: DKIM-Signature field added (s=mail, d=example.org)",
		"not a syslog line at all",
	)
	assert.Equal(t, 5.0, testutil.ToFloat64(e.foreignLogEntries))
	assert.Equal(t, 0, testutil.CollectAndCount(e.logEntries))
	assert.Equal(t, 0, testutil.CollectAndCount(e.smtpdConnects))
	assert.Equal(t, 0, testutil.CollectAndCount(e.unsupportedLogEntries))
}

func TestDefaultInstanceIgnoresRenamedSyslogName(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t)
	feed(e, "Oct  6 12:00:00 postfix-user-0 mail_user/smtpd[1]: connect from client.example.org[192.0.2.9]")
	assert.Equal(t, 1.0, testutil.ToFloat64(e.foreignLogEntries))
	assert.Equal(t, 0, testutil.CollectAndCount(e.smtpdConnects))
}

func TestMalformedLinesAreUnsupported(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t)
	feed(e,
		"",
		"Oct  6 12:00:00 host postfix/smtpd[1]: NOQUEUE: frobnicate: RCPT from client.example.org[192.0.2.9]: 554 5.7.1 Nope; from=<a@example.org> to=<x@example.net>",
		"Oct  6 12:00:00 host postfix/smtp[2]: 4AB1: to=<a@example.net>, relay=mx.example.net[192.0.2.5]:25, status=sent (250 Ok)",
		"Oct  6 12:00:00 host postfix/smtpd[3]: something postfix has never logged",
		"Oct  6 12:00:00 host postfix/frobd[4]: 4AB2: uid=x from=<root>",
	)
	assert.Equal(t, 0, testutil.CollectAndCount(e.accessActions))
	assert.Equal(t, 0, testutil.CollectAndCount(e.deliveryStatuses))
	assert.Equal(t, 0, testutil.CollectAndCount(e.messagesReceived))
	assert.Equal(t, 2.0, testutil.ToFloat64(e.unsupportedLogEntries.WithLabelValues("smtpd", "")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.unsupportedLogEntries.WithLabelValues("smtp", "")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.unsupportedLogEntries.WithLabelValues("frobd", "")))
	assert.Equal(t, 3, testutil.CollectAndCount(e.logEntries), "smtpd, smtp and frobd; the empty line is not counted")
	assert.Equal(t, 0.0, testutil.ToFloat64(e.foreignLogEntries))
}

func TestParseInstance(t *testing.T) {
	t.Parallel()
	assert.Equal(t, Instance{SyslogName: "mail_in"}, ParseInstance("mail_in"))
	assert.Equal(t, Instance{SyslogName: "postfix/relay", Service: "relay"}, ParseInstance("relay=postfix/relay"))
}

func loadConfig(t *testing.T, body string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "postfix.yml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	return cfg
}

func TestMasterFailuresAndEvents(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t, WithInstances([]Instance{{SyslogName: "mail_user"}, {SyslogName: "mail_in"}}))
	feed(e,
		"2026-10-04T23:35:20.272339+00:00 postfix-user-0 mail_user/smtpd[485]: fatal: restriction class `check_popbsmtp' needs a definition",
		"2026-10-04T23:35:21.273438+00:00 postfix-user-0 mail_user/master[100]: warning: process /usr/libexec/postfix/smtpd pid 485 exit status 1",
		"2026-10-04T23:35:21.273458+00:00 postfix-user-0 mail_user/master[100]: warning: /usr/libexec/postfix/smtpd: bad command startup -- throttling",
		"2026-10-04T23:35:22.000000+00:00 postfix-user-0 mail_user/master[100]: warning: process /usr/libexec/postfix/cleanup pid 77 killed by signal 9",
		"2026-10-04T22:48:26.398639+00:00 postfix-incoming-0 mail_in/postfix-script[114]: starting the Postfix mail system",
		"2026-10-04T22:48:26.414987+00:00 postfix-incoming-0 mail_in/master[116]: daemon started -- version 3.11.7, configuration /etc/postfix",
		"2026-10-04T22:48:36.468789+00:00 postfix-incoming-0 mail_in/master[116]: terminating on signal 15",
		"2026-10-04T22:48:36.466826+00:00 postfix-incoming-0 mail_in/postfix-script[126]: stopping the Postfix mail system",
		"2026-10-04T22:49:00.000000+00:00 postfix-incoming-0 mail_in/master[116]: reload -- version 3.11.7, configuration /etc/postfix",
	)
	assert.Equal(t, 1.0, testutil.ToFloat64(e.masterProcessFailures.WithLabelValues("smtpd", "exit")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.masterProcessFailures.WithLabelValues("cleanup", "signal")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.masterThrottles.WithLabelValues("smtpd")))
	for _, event := range []string{"starting", "started", "terminating", "stopping", "reload"} {
		assert.Equal(t, 1.0, testutil.ToFloat64(e.masterEvents.WithLabelValues(event)), event)
	}
	assert.Equal(t, 1.0, testutil.ToFloat64(e.logEntries.WithLabelValues("smtpd", "fatal")), "the fatal line itself is countable for alerts")
	assert.Equal(t, 1.0, testutil.ToFloat64(e.unsupportedLogEntries.WithLabelValues("smtpd", "fatal")))
	assert.Equal(t, 0.0, testutil.ToFloat64(e.unsupportedLogEntries.WithLabelValues("master", "")))
}

func TestMasterUnknownLinesAreUnsupported(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t)
	feed(e,
		"Oct  6 12:00:00 host postfix/postfix-script[1]: warning: not owned by root: /etc/postfix/./maps",
		"Oct  6 12:00:00 host postfix/master[2]: warning: process /usr/libexec/postfix/smtpd pid x exit status 1",
		"Oct  6 12:00:00 host postfix/master[2]: something new",
	)
	assert.Equal(t, 0, testutil.CollectAndCount(e.masterProcessFailures))
	assert.Equal(t, 0, testutil.CollectAndCount(e.masterEvents))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.unsupportedLogEntries.WithLabelValues("postfix-script", "warning")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.unsupportedLogEntries.WithLabelValues("master", "warning")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.unsupportedLogEntries.WithLabelValues("master", "")))
}

func TestConnectionTimeoutWordings(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t)
	feed(e,
		"Oct  6 12:00:00 host postfix/relay/smtp[209]: connect to 192.0.2.1[192.0.2.1]:25: Operation timed out",
		"Oct  6 12:00:00 host postfix/smtp[210]: connect to 192.0.2.2[192.0.2.2]:25: Connection timed out",
		"Oct  6 12:00:00 host postfix/smtp[211]: connect to 192.0.2.3[192.0.2.3]:25: Timed out somehow",
	)
	assert.Equal(t, 1.0, testutil.ToFloat64(e.smtpConnectionTimedOut.WithLabelValues("relay/smtp")), "musl wording")
	assert.Equal(t, 1.0, testutil.ToFloat64(e.smtpConnectionTimedOut.WithLabelValues("smtp")), "glibc wording")
	assert.Equal(t, 1.0, testutil.ToFloat64(e.unsupportedLogEntries.WithLabelValues("smtp", "")))
}

func TestNumericServiceSyslogName(t *testing.T) {
	t.Parallel()
	e := newTestExporter(t, WithInstances([]Instance{{SyslogName: "mail_in"}, {SyslogName: "mail_in/10051", Service: "10051"}}))
	feed(e,
		"Oct  7 12:00:00 postfix-incoming-0 mail_in/10051/smtpd[1]: 4AB1: client=amavis-0.amavis-headless.default.svc.cluster.local[10.192.2.7]",
		"Oct  7 12:00:00 postfix-incoming-0 mail_in/smtpd[2]: 4AB2: client=mx.example.org[192.0.2.9]",
	)
	assert.Equal(t, 1.0, testutil.ToFloat64(e.messagesReceived.WithLabelValues("10051/smtpd")))
	assert.Equal(t, 1.0, testutil.ToFloat64(e.messagesReceived.WithLabelValues("smtpd")))
}
