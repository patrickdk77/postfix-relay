// Copyright 2017 Kumina, https://kumina.nl/
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package exporter

import (
	"context"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/patrickdk77/postfix_exporter/config"
	"github.com/patrickdk77/postfix_exporter/logsource"
	"github.com/patrickdk77/postfix_exporter/showq"
	"github.com/prometheus/client_golang/prometheus"
)

// PostfixExporter holds the state that should be preserved by the
// Postfix Prometheus metrics exporter across scrapes.
type PostfixExporter struct {
	qmgrInsertsSize   prometheus.Histogram
	virtualDelivered  prometheus.Counter
	bounceDelivery    prometheus.Counter
	bounceNonDelivery prometheus.Counter

	smtpConnectionTimedOut *prometheus.CounterVec
	smtpConnectionRefused  *prometheus.CounterVec
	// same as smtpProcesses{status=deferred}, kept for compatibility
	smtpStatusDeferred prometheus.Counter
	// should be the same as smtpProcesses{status=deferred}, kept for compatibility, but this doesn't work !
	smtpDeferreds prometheus.Counter

	smtpdSASLAuthenticationFailures *prometheus.CounterVec
	smtpdFCrDNSErrors               *prometheus.CounterVec
	smtpdDisconnects                *prometheus.CounterVec
	smtpdConnects                   *prometheus.CounterVec

	cleanupProcesses   prometheus.Counter
	cleanupRejects     prometheus.Counter
	cleanupNotAccepted prometheus.Counter
	cleanupActions     *prometheus.CounterVec

	qmgrExpires            prometheus.Counter
	qmgrRemoves            prometheus.Counter
	qmgrInsertsNrcpt       prometheus.Histogram
	qmgrInsertsNrcptLegacy prometheus.Histogram
	qmgrStatuses           *prometheus.CounterVec

	logSrc logsource.LogSource

	smtpdLostConnections *prometheus.CounterVec
	smtpDeferredDSN      *prometheus.CounterVec
	smtpdProcesses       *prometheus.CounterVec
	smtpdRejects         *prometheus.CounterVec
	smtpdTLSConnects     *prometheus.CounterVec

	lmtpDelays *prometheus.HistogramVec
	pipeDelays *prometheus.HistogramVec

	smtpDelays      *prometheus.HistogramVec
	smtpTLSConnects *prometheus.CounterVec
	smtpBouncedDSN  *prometheus.CounterVec
	smtpProcesses   *prometheus.CounterVec
	smtpReplies     *prometheus.CounterVec

	accessActions         *prometheus.CounterVec
	messagesReceived      *prometheus.CounterVec
	bounceNotifications   *prometheus.CounterVec
	masterEvents          *prometheus.CounterVec
	masterProcessFailures *prometheus.CounterVec
	masterThrottles       *prometheus.CounterVec
	deliveryStatuses      *prometheus.CounterVec
	deliveryDelays        *prometheus.HistogramVec
	deliveryStageDelays   *prometheus.HistogramVec
	deliveryStatusReplies *prometheus.CounterVec

	unsupportedLogEntries *prometheus.CounterVec
	logEntries            *prometheus.CounterVec
	foreignLogEntries     prometheus.Counter

	postfixUp *prometheus.GaugeVec

	showq *showq.Showq

	postscreenConnects         prometheus.Counter
	postscreenConnectsRejected *prometheus.CounterVec
	postscreenPasses           *prometheus.CounterVec
	postscreenDNSBLRank        prometheus.Histogram
	postscreenRejects          *prometheus.CounterVec
	postscreenTests            *prometheus.CounterVec
	postscreenHangups          prometheus.Counter
	postscreenAccessList       *prometheus.CounterVec

	bounceLabels     []string
	cleanupLabels    []string
	smtpLabels       []string
	smtpdLabels      []string
	virtualLabels    []string
	qmgrLabels       []string
	pipeLabels       []string
	lmtpLabels       []string
	postscreenLabels []string

	instances []Instance
	services  map[string]string
	logLine   *regexp.Regexp
	config    *config.Config

	once sync.Once

	logUnsupportedLines bool
}

// ServiceLabel is a function to apply user-defined service labels to PostfixExporter.
type ServiceLabel func(*PostfixExporter)

// Patterns for parsing log messages.
var (
	lmtpPipeSMTPLine                    = regexp.MustCompile(`, relay=(\S+), .*, delays=([0-9\.]+)/([0-9\.]+)/([0-9\.]+)/([0-9\.]+), `)
	qmgrInsertLine                      = regexp.MustCompile(`:.*, size=(\d+), nrcpt=(\d+) `)
	qmgrExpiredLine                     = regexp.MustCompile(`:.*, status=(expired|force-expired), returned to sender`)
	qmgrStatusLine                      = regexp.MustCompile(`, status=([a-z-]+), `)
	statusLine                          = regexp.MustCompile(`, status=(\w+) `)
	smtpDSNLine                         = regexp.MustCompile(`, dsn=(\d\.\d+\.\d+)`)
	smtpTLSLine                         = regexp.MustCompile(`^(\S+) TLS connection established to \S+: (\S+) with cipher (\S+) \((\d+)/(\d+) bits\)`)
	smtpConnectionTimedOut              = regexp.MustCompile(`^connect\s+to\s+(.*)\[(.*)\]:(\d+):\s+(Connection timed out|Operation timed out)$`)
	smtpConnectionRefused               = regexp.MustCompile(`connect\s+to\s+(.*)\[(.*)\]:(\d+):\s+(Connection refused)$`)
	smtpdFCrDNSErrorsLine               = regexp.MustCompile(`^warning: hostname \S+ does not resolve to address `)
	smtpdProcessesSASLLine              = regexp.MustCompile(`: client=.*, sasl_method=(\S+)`)
	smtpdRejectsLine                    = regexp.MustCompile(`^NOQUEUE: reject: RCPT from \S+: ([0-9]+) `)
	smtpdLostConnectionLine             = regexp.MustCompile(`^(?:NOQUEUE: )?lost connection after (\w+) from `)
	smtpdSASLAuthenticationFailuresLine = regexp.MustCompile(`^warning: \S+: SASL (\S+) authentication failed: `)
	smtpdTLSLine                        = regexp.MustCompile(`^(\S+) TLS connection established from \S+: (\S+) with cipher (\S+) \((\d+)/(\d+) bits\)`)
	bounceNonDeliveryLine               = regexp.MustCompile(`: sender non-delivery notification: `)
	bounceDeliveryLine                  = regexp.MustCompile(`: sender delivery status notification: `)
	postscreenPassLine                  = regexp.MustCompile(`^PASS (NEW|OLD) `)
	postscreenDNSBLLine                 = regexp.MustCompile(`^DNSBL rank (\d+) for `)
	postscreenConnectRejectLine         = regexp.MustCompile(`^NOQUEUE: reject: CONNECT from \S+: (.+)$`)
	postscreenRejectLine                = regexp.MustCompile(`^NOQUEUE: reject: RCPT from \S+: ([0-9]+) `)
	postscreenTestLine                  = regexp.MustCompile(`^(PREGREET|NON-SMTP COMMAND|COMMAND PIPELINING|COMMAND TIME LIMIT|COMMAND COUNT LIMIT|COMMAND LENGTH LIMIT|BARE NEWLINE) `)
	postscreenHangupLine                = regexp.MustCompile(`^HANGUP `)
	postscreenAccessListLine            = regexp.MustCompile(`^(ALLOWLISTED|WHITELISTED|DENYLISTED|BLACKLISTED) `)

	// Access actions from smtpd, postscreen and cleanup, at any SMTP
	// stage, such as "NOQUEUE: reject: RCPT from host[1.2.3.4]: 554 ...".
	accessActionLine = regexp.MustCompile(`^(?:NOQUEUE|[0-9A-Za-z]+): (reject|reject_warning|discard|hold|filter|redirect|warn|info|milter-reject|milter-discard|milter-hold|milter-redirect|milter-quarantine): ([A-Z][A-Z-]*) from \S+: (.*)$`)
	// header_checks and body_checks actions logged by cleanup.
	cleanupActionLine = regexp.MustCompile(`^[0-9A-Za-z]+: (reject|discard|hold|warning|info|filter|redirect|replace|prepend|bcc|strip): (header|body|mime-header|nested-header) `)
	// Delivery status from any delivery agent: smtp, lmtp, local,
	// virtual, pipe, error, discard and so on.
	deliveryStatusLine = regexp.MustCompile(`\bdelay=(-?[\d.]+),.*\bdsn=(\d\.\d{1,3}\.\d{1,3}), status=([a-z-]+) \((.*)\)$`)
	deliveryDelaysLine = regexp.MustCompile(`\bdelays=([\d.]+)/([\d.]+)/([\d.]+)/([\d.]+)`)
	// A message entering Postfix: smtpd and qmqpd log client=, pickup
	// logs uid= for mail submitted with sendmail or postdrop.
	receivedLine = regexp.MustCompile(`^[0-9A-Za-z]+: (?:client=|uid=\d+ from=<)`)
	// Notifications from the bounce daemon, which runs the bounce, defer
	// and trace services.
	bounceNotificationLine = regexp.MustCompile(`^[0-9A-Za-z]+: (sender|postmaster) (non-delivery|delay|delivery status|success) notification: `)
	// postfix master and postfix-script lifecycle and child failures.
	masterEventLine       = regexp.MustCompile(`^(daemon started|reload|terminating on signal|starting the Postfix mail system|stopping the Postfix mail system|refreshing the Postfix mail system)\b`)
	masterProcessFailLine = regexp.MustCompile(`^warning: process (\S+) pid \d+ (exit status|killed by signal) \d+`)
	masterThrottleLine    = regexp.MustCompile(`^warning: (\S+): bad command startup -- throttling`)
	smtpHostSaidLine      = regexp.MustCompile(`^[0-9A-Za-z]+: (host \S+ said: .+ \(in reply to \w+[\w /-]*\))$`)
)

func matchesLabel(labels []string, subprocess, daemon string) bool {
	return slices.Contains(labels, subprocess) || slices.Contains(labels, daemon)
}

func (e *PostfixExporter) collectFromPostfixLogLine(line, subprocess, level, remainder string) {
	generic := e.collectGenericLog(subprocess, remainder)
	daemon := subprocess[strings.LastIndexByte(subprocess, '/')+1:]

	handled := false
	service := subprocess
	switch {
	case daemon == "master" || daemon == "postfix-script":
		service, handled = daemon, e.collectMasterLog(remainder)
	case matchesLabel(e.cleanupLabels, subprocess, daemon):
		service, handled = "cleanup", e.collectCleanupLog(remainder)
	case matchesLabel(e.lmtpLabels, subprocess, daemon):
		service, handled = "lmtp", e.collectLMTPLog(subprocess, remainder)
	case matchesLabel(e.pipeLabels, subprocess, daemon):
		service, handled = "pipe", e.collectPipeLog(subprocess, remainder)
	case matchesLabel(e.qmgrLabels, subprocess, daemon):
		service, handled = "qmgr", e.collectQmgrLog(remainder)
	case matchesLabel(e.smtpLabels, subprocess, daemon):
		service, handled = "smtp", e.collectSMTPLog(subprocess, remainder)
	case matchesLabel(e.smtpdLabels, subprocess, daemon):
		service, handled = "smtpd", e.collectSMTPdLog(subprocess, remainder)
	case matchesLabel(e.bounceLabels, subprocess, daemon):
		service, handled = "postfix", e.collectBounceLog(remainder)
	case matchesLabel(e.virtualLabels, subprocess, daemon):
		service, handled = "postfix", e.collectVirtualLog(remainder)
	case matchesLabel(e.postscreenLabels, subprocess, daemon):
		service, handled = "postscreen", e.collectPostscreenLog(remainder)
	}
	if !handled && !generic {
		e.addToUnsupportedLine(line, service, level)
	}
}

// collectGenericLog collects the metrics that do not depend on which
// daemon logged the line, and reports whether the line matched any.
func (e *PostfixExporter) collectGenericLog(subprocess, remainder string) bool {
	matched := false
	if receivedLine.MatchString(remainder) {
		e.messagesReceived.WithLabelValues(subprocess).Inc()
		matched = true
	}
	if m := bounceNotificationLine.FindStringSubmatch(remainder); m != nil {
		e.bounceNotifications.WithLabelValues(subprocess, m[1], m[2]).Inc()
		matched = true
	}
	if m := accessActionLine.FindStringSubmatch(remainder); m != nil {
		reply := parseActionReply(m[3])
		text := replyText(e.config.RejectReplies, reply)
		e.accessActions.WithLabelValues(subprocess, m[1], m[2], reply.Code, reply.EnhancedCode, text).Inc()
		matched = true
	}
	if m := deliveryStatusLine.FindStringSubmatch(remainder); m != nil {
		status := m[3]
		e.deliveryStatuses.WithLabelValues(subprocess, status).Inc()
		addToHistogramVec(e.deliveryDelays, m[1], "delivery delay", subprocess, status)
		if d := deliveryDelaysLine.FindStringSubmatch(remainder); d != nil {
			addToHistogramVec(e.deliveryStageDelays, d[1], "delivery pdelay", subprocess, "before_queue_manager")
			addToHistogramVec(e.deliveryStageDelays, d[2], "delivery adelay", subprocess, "queue_manager")
			addToHistogramVec(e.deliveryStageDelays, d[3], "delivery sdelay", subprocess, "connection_setup")
			addToHistogramVec(e.deliveryStageDelays, d[4], "delivery xdelay", subprocess, "transmission")
		}
		reply := parseHostReply(m[4], m[2])
		text := statusReplyText(e.config.StatusReplies, status, reply)
		e.deliveryStatusReplies.WithLabelValues(subprocess, status, reply.Code, reply.EnhancedCode, text).Inc()
		matched = true
	}
	if m := smtpHostSaidLine.FindStringSubmatch(remainder); m != nil {
		reply := parseHostReply(m[1], "")
		text := replyText(e.config.SmtpReplies, reply)
		e.smtpReplies.WithLabelValues(reply.Code, reply.EnhancedCode, text).Inc()
		matched = true
	}
	return matched
}

var masterEvents = map[string]string{
	"daemon started":                     "started",
	"reload":                             "reload",
	"terminating on signal":              "terminating",
	"starting the Postfix mail system":   "starting",
	"stopping the Postfix mail system":   "stopping",
	"refreshing the Postfix mail system": "refreshing",
}

func (e *PostfixExporter) collectMasterLog(remainder string) bool {
	if m := masterEventLine.FindStringSubmatch(remainder); m != nil {
		e.masterEvents.WithLabelValues(masterEvents[m[1]]).Inc()
	} else if m := masterProcessFailLine.FindStringSubmatch(remainder); m != nil {
		reason := "exit"
		if m[2] == "killed by signal" {
			reason = "signal"
		}
		e.masterProcessFailures.WithLabelValues(m[1][strings.LastIndexByte(m[1], '/')+1:], reason).Inc()
	} else if m := masterThrottleLine.FindStringSubmatch(remainder); m != nil {
		e.masterThrottles.WithLabelValues(m[1][strings.LastIndexByte(m[1], '/')+1:]).Inc()
	} else {
		return false
	}
	return true
}

func (e *PostfixExporter) collectCleanupLog(remainder string) bool {
	switch {
	case strings.Contains(remainder, ": message-id=<"):
		e.cleanupProcesses.Inc()
	case strings.Contains(remainder, ": reject: "):
		e.cleanupRejects.Inc()
		if m := cleanupActionLine.FindStringSubmatch(remainder); m != nil {
			e.cleanupActions.WithLabelValues(m[1], m[2]).Inc()
		}
	default:
		m := cleanupActionLine.FindStringSubmatch(remainder)
		if m == nil {
			return false
		}
		e.cleanupActions.WithLabelValues(m[1], m[2]).Inc()
	}
	return true
}

func (e *PostfixExporter) collectLMTPLog(subprocess, remainder string) bool {
	lmtpMatches := lmtpPipeSMTPLine.FindStringSubmatch(remainder)
	if lmtpMatches == nil {
		return false
	}
	statusMatches := statusLine.FindStringSubmatch(remainder)
	if statusMatches == nil || statusMatches[1] == "deferred" {
		return true
	}
	addToHistogramVec(e.lmtpDelays, lmtpMatches[2], "LMTP pdelay", subprocess, "before_queue_manager")
	addToHistogramVec(e.lmtpDelays, lmtpMatches[3], "LMTP adelay", subprocess, "queue_manager")
	addToHistogramVec(e.lmtpDelays, lmtpMatches[4], "LMTP sdelay", subprocess, "connection_setup")
	addToHistogramVec(e.lmtpDelays, lmtpMatches[5], "LMTP xdelay", subprocess, "transmission")
	return true
}

func (e *PostfixExporter) collectPipeLog(subprocess, remainder string) bool {
	pipeMatches := lmtpPipeSMTPLine.FindStringSubmatch(remainder)
	if pipeMatches == nil {
		return false
	}
	addToHistogramVec(e.pipeDelays, pipeMatches[2], "PIPE pdelay", subprocess, pipeMatches[1], "before_queue_manager")
	addToHistogramVec(e.pipeDelays, pipeMatches[3], "PIPE adelay", subprocess, pipeMatches[1], "queue_manager")
	addToHistogramVec(e.pipeDelays, pipeMatches[4], "PIPE sdelay", subprocess, pipeMatches[1], "connection_setup")
	addToHistogramVec(e.pipeDelays, pipeMatches[5], "PIPE xdelay", subprocess, pipeMatches[1], "transmission")
	return true
}

func (e *PostfixExporter) collectQmgrLog(remainder string) bool {
	if m := qmgrStatusLine.FindStringSubmatch(remainder); m != nil {
		e.qmgrStatuses.WithLabelValues(m[1]).Inc()
	}
	qmgrInsertMatches := qmgrInsertLine.FindStringSubmatch(remainder)
	switch {
	case qmgrInsertMatches != nil:
		addToHistogram(e.qmgrInsertsSize, qmgrInsertMatches[1], "QMGR size")
		addToHistogram(e.qmgrInsertsNrcptLegacy, qmgrInsertMatches[2], "QMGR nrcpt")
		addToHistogram(e.qmgrInsertsNrcpt, qmgrInsertMatches[2], "QMGR nrcpt")
	case strings.HasSuffix(remainder, ": removed"):
		e.qmgrRemoves.Inc()
	case qmgrExpiredLine.MatchString(remainder):
		e.qmgrExpires.Inc()
	default:
		return false
	}
	return true
}

func (e *PostfixExporter) collectSMTPLog(subprocess, remainder string) bool {
	if smtpMatches := lmtpPipeSMTPLine.FindStringSubmatch(remainder); smtpMatches != nil {
		e.collectSMTPStatusLog(subprocess, remainder, smtpMatches)
	} else if smtpTLSMatches := smtpTLSLine.FindStringSubmatch(remainder); smtpTLSMatches != nil {
		e.smtpTLSConnects.WithLabelValues(append([]string{subprocess}, smtpTLSMatches[1:]...)...).Inc()
	} else if connectionTimedOutMatches := smtpConnectionTimedOut.FindStringSubmatch(remainder); connectionTimedOutMatches != nil {
		e.smtpConnectionTimedOut.WithLabelValues(subprocess).Inc()
	} else if connectionRefusedMatches := smtpConnectionRefused.FindStringSubmatch(remainder); connectionRefusedMatches != nil {
		e.smtpConnectionRefused.WithLabelValues(subprocess).Inc()
	} else {
		return false
	}
	return true
}

func (e *PostfixExporter) collectSMTPStatusLog(subprocess, remainder string, delays []string) {
	smtpStatusMatches := statusLine.FindStringSubmatch(remainder)
	if smtpStatusMatches == nil {
		return
	}
	e.smtpProcesses.WithLabelValues(subprocess, smtpStatusMatches[1]).Inc()
	dsnMatches := smtpDSNLine.FindStringSubmatch(remainder)
	switch smtpStatusMatches[1] {
	case "deferred":
		e.smtpStatusDeferred.Inc()
		if dsnMatches != nil {
			e.smtpDeferredDSN.WithLabelValues(subprocess, dsnMatches[1]).Inc()
		}
	case "bounced":
		if dsnMatches != nil {
			e.smtpBouncedDSN.WithLabelValues(subprocess, dsnMatches[1]).Inc()
		}
		fallthrough
	default:
		addToHistogramVec(e.smtpDelays, delays[2], "SMTP pdelay", subprocess, "before_queue_manager")
		addToHistogramVec(e.smtpDelays, delays[3], "SMTP adelay", subprocess, "queue_manager")
		addToHistogramVec(e.smtpDelays, delays[4], "SMTP sdelay", subprocess, "connection_setup")
		addToHistogramVec(e.smtpDelays, delays[5], "SMTP xdelay", subprocess, "transmission")
	}
}

func (e *PostfixExporter) collectSMTPdLog(subprocess, remainder string) bool {
	if strings.HasPrefix(remainder, "connect from ") {
		e.smtpdConnects.WithLabelValues(subprocess).Inc()
	} else if strings.HasPrefix(remainder, "disconnect from ") {
		e.smtpdDisconnects.WithLabelValues(subprocess).Inc()
	} else if smtpdFCrDNSErrorsLine.MatchString(remainder) {
		e.smtpdFCrDNSErrors.WithLabelValues(subprocess).Inc()
	} else if smtpdLostConnectionMatches := smtpdLostConnectionLine.FindStringSubmatch(remainder); smtpdLostConnectionMatches != nil {
		e.smtpdLostConnections.WithLabelValues(subprocess, smtpdLostConnectionMatches[1]).Inc()
	} else if smtpdProcessesSASLMatches := smtpdProcessesSASLLine.FindStringSubmatch(remainder); smtpdProcessesSASLMatches != nil {
		e.smtpdProcesses.WithLabelValues(subprocess, strings.ReplaceAll(smtpdProcessesSASLMatches[1], ",", "")).Inc()
	} else if strings.Contains(remainder, ": client=") {
		e.smtpdProcesses.WithLabelValues(subprocess, "NONE").Inc()
	} else if smtpdRejectsMatches := smtpdRejectsLine.FindStringSubmatch(remainder); smtpdRejectsMatches != nil {
		e.smtpdRejects.WithLabelValues(subprocess, smtpdRejectsMatches[1]).Inc()
	} else if saslMatches := smtpdSASLAuthenticationFailuresLine.FindStringSubmatch(remainder); saslMatches != nil {
		e.smtpdSASLAuthenticationFailures.WithLabelValues(subprocess, saslMatches[1]).Inc()
	} else if smtpdTLSMatches := smtpdTLSLine.FindStringSubmatch(remainder); smtpdTLSMatches != nil {
		e.smtpdTLSConnects.WithLabelValues(append([]string{subprocess}, smtpdTLSMatches[1:]...)...).Inc()
	} else {
		return false
	}
	return true
}

func (e *PostfixExporter) collectBounceLog(remainder string) bool {
	if ndnMatches := bounceNonDeliveryLine.FindStringSubmatch(remainder); ndnMatches != nil {
		e.bounceNonDelivery.Inc()
	} else if dsnMatches := bounceDeliveryLine.FindStringSubmatch(remainder); dsnMatches != nil {
		e.bounceDelivery.Inc()
	} else {
		return false
	}
	return true
}

func (e *PostfixExporter) collectVirtualLog(remainder string) bool {
	if !strings.HasSuffix(remainder, ", status=sent (delivered to maildir)") {
		return false
	}
	e.virtualDelivered.Inc()
	return true
}

func (e *PostfixExporter) collectPostscreenLog(remainder string) bool {
	if strings.HasPrefix(remainder, "CONNECT from ") {
		e.postscreenConnects.Inc()
	} else if rejectMatches := postscreenConnectRejectLine.FindStringSubmatch(remainder); rejectMatches != nil {
		e.postscreenConnectsRejected.WithLabelValues(rejectMatches[1]).Inc()
	} else if passMatches := postscreenPassLine.FindStringSubmatch(remainder); passMatches != nil {
		e.postscreenPasses.WithLabelValues(strings.ToLower(passMatches[1])).Inc()
	} else if dnsblMatches := postscreenDNSBLLine.FindStringSubmatch(remainder); dnsblMatches != nil {
		addToHistogram(e.postscreenDNSBLRank, dnsblMatches[1], "postscreen DNSBL rank")
	} else if rejectMatches := postscreenRejectLine.FindStringSubmatch(remainder); rejectMatches != nil {
		e.postscreenRejects.WithLabelValues(rejectMatches[1]).Inc()
	} else if postscreenHangupLine.MatchString(remainder) {
		e.postscreenHangups.Inc()
	} else if testMatches := postscreenTestLine.FindStringSubmatch(remainder); testMatches != nil {
		test := strings.ReplaceAll(strings.ReplaceAll(testMatches[1], "-", "_"), " ", "_")
		e.postscreenTests.WithLabelValues(test).Inc()
	} else if accessListMatches := postscreenAccessListLine.FindStringSubmatch(remainder); accessListMatches != nil {
		action := "allow"
		if accessListMatches[1] == "DENYLISTED" || accessListMatches[1] == "BLACKLISTED" {
			action = "deny"
		}
		e.postscreenAccessList.WithLabelValues(action).Inc()
	} else {
		return false
	}
	return true
}

// CollectFromLogline collects metrict from a Postfix log line.
func (e *PostfixExporter) CollectFromLogLine(line string) {
	if line == "" {
		return
	}
	// Strip off timestamp, hostname, etc.
	logMatches := e.logLine.FindStringSubmatch(line)

	if logMatches == nil {
		// Not logged by a configured Postfix instance.
		e.foreignLogEntries.Inc()
		return
	}
	process := logMatches[1]
	level := logMatches[5]
	remainder := logMatches[4]
	subprocess := logMatches[3]
	if service := e.services[process]; service != "" {
		subprocess = strings.TrimSuffix(service+"/"+subprocess, "/")
	}
	severity := level
	if severity == "" {
		severity = "info"
	}
	e.logEntries.WithLabelValues(subprocess, severity).Inc()
	e.collectFromPostfixLogLine(line, subprocess, level, remainder)
}

func (e *PostfixExporter) addToUnsupportedLine(line string, subprocess string, level string) {
	if e.logUnsupportedLines {
		slog.Warn("Unsupported Line", "line", line)
	}
	e.unsupportedLogEntries.WithLabelValues(subprocess, level).Inc()
}

func addToHistogram(h prometheus.Histogram, value, fieldName string) {
	float, err := strconv.ParseFloat(value, 64)
	if err != nil {
		slog.Error("Couldn't convert value for histogram", "value", value, "field", fieldName, "error", err)
	}
	h.Observe(float)
}
func addToHistogramVec(h *prometheus.HistogramVec, value, fieldName string, labels ...string) {
	float, err := strconv.ParseFloat(value, 64)
	if err != nil {
		slog.Error("Couldn't convert value for histogram vector", "value", value, "field", fieldName, "error", err)
	}
	h.WithLabelValues(labels...).Observe(float)
}

var (
	defaultCleanupLabels    = []string{"cleanup"}
	defaultLmtpLabels       = []string{"lmtp"}
	defaultPipeLabels       = []string{"pipe"}
	defaultQmgrLabels       = []string{"qmgr"}
	defaultSmtpLabels       = []string{"smtp"}
	defaultSmtpdLabels      = []string{"smtpd"}
	defaultBounceLabels     = []string{"bounce"}
	defaultVirtualLabels    = []string{"virtual"}
	defaultPostscreenLabels = []string{"postscreen"}
	defaultInstances        = []Instance{{SyslogName: "postfix"}}
)

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithCleanupLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.cleanupLabels = labels
	}
}

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithLmtpLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.lmtpLabels = labels
	}
}

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithPipeLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.pipeLabels = labels
	}
}

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithQmgrLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.qmgrLabels = labels
	}
}

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithSmtpLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.smtpLabels = labels
	}
}

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithSmtpdLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.smtpdLabels = labels
	}
}

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithBounceLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.bounceLabels = labels
	}
}

// WithCleanupLabels is a function to apply user-defined service labels to PostfixExporter.
func WithVirtualLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.virtualLabels = labels
	}
}

// WithPostscreenLabels is a function to apply user-defined service labels to PostfixExporter.
func WithPostscreenLabels(labels []string) ServiceLabel {
	return func(e *PostfixExporter) {
		e.postscreenLabels = labels
	}
}

// Instance is a syslog name whose lines are parsed. Service is the
// master.cf service that set it with -o syslog_name, and is prefixed to
// the subprogram label, so "postfix/relay/smtp" with Service "relay"
// reports as "relay/smtp". Service is empty for main.cf's syslog_name.
type Instance struct {
	SyslogName string
	Service    string
}

// ParseInstance reads "name" or "service=name".
func ParseInstance(s string) Instance {
	if service, name, ok := strings.Cut(s, "="); ok {
		return Instance{SyslogName: name, Service: service}
	}
	return Instance{SyslogName: s}
}

// WithInstances sets the syslog names whose lines are parsed, such as
// "postfix" or a syslog_name like "mail_in". Lines from any other
// program count as foreign.
func WithInstances(instances []Instance) ServiceLabel {
	return func(e *PostfixExporter) {
		if len(instances) > 0 {
			e.instances = instances
		}
	}
}

// WithConfig sets the reply matching rules used for text labels.
func WithConfig(cfg *config.Config) ServiceLabel {
	return func(e *PostfixExporter) {
		if cfg != nil {
			e.config = cfg
		}
	}
}

func logLinePattern(instances []Instance) *regexp.Regexp {
	// Longest first, so "postfix/relay" wins over "postfix".
	sorted := slices.Clone(instances)
	slices.SortStableFunc(sorted, func(a, b Instance) int { return len(b.SyslogName) - len(a.SyslogName) })
	names := make([]string, 0, len(sorted))
	for _, instance := range sorted {
		names = append(names, regexp.QuoteMeta(instance.SyslogName))
	}
	return regexp.MustCompile(`(?:^|\s)(` + strings.Join(names, "|") + `)(/([\w_\.+/-]+))?\[\d+\]: ((?:(warning|error|fatal|panic): )?.*)`)
}

func (e *PostfixExporter) init() {
	timeBuckets := []float64{1e-3, 1e-2, 1e-1, 1.0, 10, 1 * 60, 1 * 60 * 60, 24 * 60 * 60, 2 * 24 * 60 * 60}

	e.once.Do(func() {
		e.logLine = logLinePattern(e.instances)
		e.services = make(map[string]string, len(e.instances))
		for _, instance := range e.instances {
			e.services[instance.SyslogName] = instance.Service
		}
		constLabels := logsource.LogSourceDefaults{}.ConstLabels()
		if e.logSrc != nil {
			constLabels = e.logSrc.ConstLabels()
		}
		e.cleanupProcesses = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "cleanup_messages_processed_total",
			Help:        "Total number of messages processed by cleanup.",
			ConstLabels: constLabels,
		})
		e.cleanupRejects = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "cleanup_messages_rejected_total",
			Help:        "Total number of messages rejected by cleanup.",
			ConstLabels: constLabels,
		})
		e.cleanupNotAccepted = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "cleanup_messages_not_accepted_total",
			Help:        "Total number of messages not accepted by cleanup.",
			ConstLabels: constLabels,
		})
		e.cleanupActions = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "cleanup_actions_total",
				Help:        "Total number of header_checks and body_checks actions taken by cleanup, by action and message part.",
				ConstLabels: constLabels,
			},
			[]string{"action", "part"})
		e.lmtpDelays = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace:   "postfix",
				Name:        "lmtp_delivery_delay_seconds",
				Help:        "LMTP message processing time in seconds.",
				Buckets:     timeBuckets,
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "stage"})
		e.pipeDelays = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace:   "postfix",
				Name:        "pipe_delivery_delay_seconds",
				Help:        "Pipe message processing time in seconds.",
				Buckets:     timeBuckets,
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "relay", "stage"})
		// Metric name contains a typo, "receipients", metric is kept to ensure backwards compatibility
		e.qmgrInsertsNrcptLegacy = prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace:   "postfix",
			Name:        "qmgr_messages_inserted_receipients",
			Help:        "Legacy metric, please switch to postfix_qmgr_messages_inserted_recipients.",
			Buckets:     []float64{1, 2, 4, 8, 16, 32, 64, 128},
			ConstLabels: constLabels,
		})
		e.qmgrInsertsNrcpt = prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace:   "postfix",
			Name:        "qmgr_messages_inserted_recipients",
			Help:        "Number of recipients per message inserted into the mail queues.",
			Buckets:     []float64{1, 2, 4, 8, 16, 32, 64, 128},
			ConstLabels: constLabels,
		})
		e.qmgrInsertsSize = prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace:   "postfix",
			Name:        "qmgr_messages_inserted_size_bytes",
			Help:        "Size of messages inserted into the mail queues in bytes.",
			Buckets:     []float64{1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9},
			ConstLabels: constLabels,
		})
		e.qmgrRemoves = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "qmgr_messages_removed_total",
			Help:        "Total number of messages removed from mail queues.",
			ConstLabels: constLabels,
		})
		e.qmgrExpires = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "qmgr_messages_expired_total",
			Help:        "Total number of messages expired from mail queues.",
			ConstLabels: constLabels,
		})
		e.qmgrStatuses = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "qmgr_statuses_total",
				Help:        "Total number of message status changes logged by the queue manager, by status.",
				ConstLabels: constLabels,
			},
			[]string{"status"})
		e.smtpDelays = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace:   "postfix",
				Name:        "smtp_delivery_delay_seconds",
				Help:        "SMTP message processing time in seconds.",
				Buckets:     timeBuckets,
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "stage"})
		e.smtpTLSConnects = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtp_tls_connections_total",
				Help:        "Total number of outgoing TLS connections.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "trust", "protocol", "cipher", "secret_bits", "algorithm_bits"})
		e.smtpDeferreds = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "smtp_deferred_messages_total",
			Help:        "Total number of messages that have been deferred on SMTP.",
			ConstLabels: constLabels,
		})
		e.smtpProcesses = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtp_messages_processed_total",
				Help:        "Total number of messages that have been processed by the smtp process.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "status"})
		e.smtpDeferredDSN = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtp_deferred_messages_by_dsn_total",
				Help:        "Total number of messages that have been deferred on SMTP by DSN.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "dsn"})
		e.smtpBouncedDSN = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtp_bounced_messages_by_dsn_total",
				Help:        "Total number of messages that have been bounced on SMTP by DSN.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "dsn"})
		e.smtpConnectionTimedOut = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtp_connection_timed_out_total",
				Help:        "Total number of messages that have been deferred on SMTP.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram"})
		e.smtpConnectionRefused = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtp_connection_refused_total",
				Help:        "Total number of messages that have been refused on SMTP.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram"})
		e.smtpReplies = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtp_replies_total",
				Help:        "Total number of remote server replies logged without a delivery status, by code. The text label comes from the smtp_replies config rules.",
				ConstLabels: constLabels,
			},
			[]string{"code", "enhanced_code", "text"})
		e.smtpdConnects = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtpd_connects_total",
				Help:        "Total number of incoming connections.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram"})
		e.smtpdDisconnects = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtpd_disconnects_total",
				Help:        "Total number of incoming disconnections.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram"})
		e.smtpdFCrDNSErrors = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtpd_forward_confirmed_reverse_dns_errors_total",
				Help:        "Total number of connections for which forward-confirmed DNS cannot be resolved.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram"})
		e.smtpdLostConnections = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtpd_connections_lost_total",
				Help:        "Total number of connections lost.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "after_stage"})
		e.smtpdProcesses = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtpd_messages_processed_total",
				Help:        "Total number of messages processed.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "sasl_method"})
		e.smtpdRejects = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtpd_messages_rejected_total",
				Help:        "Total number of NOQUEUE rejects.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "code"})
		e.smtpdSASLAuthenticationFailures = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtpd_sasl_authentication_failures_total",
				Help:        "Total number of SASL authentication failures, by SASL method.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "method"})
		e.smtpdTLSConnects = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "smtpd_tls_connections_total",
				Help:        "Total number of incoming TLS connections.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "trust", "protocol", "cipher", "secret_bits", "algorithm_bits"})
		e.accessActions = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "access_actions_total",
				Help:        "Total number of access actions such as reject, reject_warning, discard, hold and milter-reject, by daemon, action, SMTP command and reply code. The text label comes from the reject_replies config rules.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "action", "command", "code", "enhanced_code", "text"})
		e.messagesReceived = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "messages_received_total",
				Help:        "Total number of messages entering Postfix, by input: an smtpd or qmqpd service, or pickup for mail submitted with sendmail or postdrop.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram"})
		e.bounceNotifications = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "bounce_notifications_total",
				Help:        "Total number of notifications sent by the bounce daemon, by recipient (sender or postmaster) and type (non-delivery, delay, delivery status).",
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "recipient", "type"})
		e.masterEvents = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "master_events_total",
				Help:        "Total number of Postfix start, reload and stop events logged by master and postfix-script.",
				ConstLabels: constLabels,
			},
			[]string{"event"})
		e.masterProcessFailures = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "master_process_failures_total",
				Help:        "Total number of Postfix daemon processes that exited with an error or were killed, by program. A daemon failing on bad configuration shows here.",
				ConstLabels: constLabels,
			},
			[]string{"program", "reason"})
		e.masterThrottles = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "master_throttles_total",
				Help:        "Total number of times master throttled a service because its program failed at startup.",
				ConstLabels: constLabels,
			},
			[]string{"program"})
		e.deliveryStatuses = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "delivery_statuses_total",
				Help:        "Total number of delivery attempts by delivery agent and status (sent, deferred, bounced, expired and so on).",
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "status"})
		e.deliveryDelays = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace:   "postfix",
				Name:        "delivery_delay_seconds",
				Help:        "Total time from message arrival to the delivery attempt, by delivery agent and status.",
				Buckets:     timeBuckets,
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "status"})
		e.deliveryStageDelays = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace:   "postfix",
				Name:        "delivery_stage_delay_seconds",
				Help:        "Time spent in each delivery stage (before the queue manager, in the queue manager, connection setup and transmission), by delivery agent.",
				Buckets:     timeBuckets,
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "stage"})
		e.deliveryStatusReplies = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "delivery_status_replies_total",
				Help:        "Total number of delivery attempts by delivery agent, status and reply code. The text label comes from the status_replies config rules.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "status", "code", "enhanced_code", "text"})
		e.unsupportedLogEntries = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "unsupported_log_entries_total",
				Help:        "Log entries that could not be processed.",
				ConstLabels: constLabels,
			},
			[]string{"service", "level"})
		e.logEntries = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "log_entries_total",
				Help:        "Total number of Postfix log entries processed, by daemon and severity.",
				ConstLabels: constLabels,
			},
			[]string{"subprogram", "severity"})
		e.foreignLogEntries = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "foreign_log_entries_total",
			Help:        "Total number of log entries from programs other than the configured Postfix instances.",
			ConstLabels: constLabels,
		})
		e.smtpStatusDeferred = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "smtp_status_deferred",
			Help:        "Total number of messages deferred.",
			ConstLabels: constLabels,
		})
		e.bounceNonDelivery = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "bounce_non_delivery_notification_total",
			Help:        "Total number of non delivery notification sent by bounce.",
			ConstLabels: constLabels,
		})
		e.bounceDelivery = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "bounce_delivery_status_notification_total",
			Help:        "Total number of delivery status notification sent by bounce.",
			ConstLabels: constLabels,
		})
		e.virtualDelivered = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "virtual_delivered_total",
			Help:        "Total number of mail delivered to a virtual mailbox.",
			ConstLabels: constLabels,
		})
		e.postscreenConnects = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "postscreen_connects_total",
			Help:        "Total number of connections handled by postscreen.",
			ConstLabels: constLabels,
		})
		e.postscreenConnectsRejected = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "postscreen_connects_rejected_total",
				Help:        "Total number of connections rejected by postscreen, by reject reason.",
				ConstLabels: constLabels,
			},
			[]string{"reason"})
		e.postscreenPasses = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "postscreen_passes_total",
				Help:        "Total number of clients passed by postscreen, by allowlist status (new/old).",
				ConstLabels: constLabels,
			},
			[]string{"type"})
		e.postscreenDNSBLRank = prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace:   "postfix",
			Name:        "postscreen_dnsbl_rank",
			Help:        "DNSBL rank assigned to clients by postscreen.",
			Buckets:     []float64{0, 1, 2, 3, 4, 5, 6, 8, 10, 12, 15},
			ConstLabels: constLabels,
		})
		e.postscreenRejects = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "postscreen_messages_rejected_total",
				Help:        "Total number of NOQUEUE rejects issued by postscreen, by response code.",
				ConstLabels: constLabels,
			},
			[]string{"code"})
		e.postscreenTests = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "postscreen_tests_failed_total",
				Help:        "Total number of postscreen pre-greet/protocol test failures, by test name.",
				ConstLabels: constLabels,
			},
			[]string{"test"})
		e.postscreenHangups = prometheus.NewCounter(prometheus.CounterOpts{
			Namespace:   "postfix",
			Name:        "postscreen_hangups_total",
			Help:        "Total client disconnects reported by postscreen while testing.",
			ConstLabels: constLabels,
		})
		e.postscreenAccessList = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace:   "postfix",
				Name:        "postscreen_access_list_matches_total",
				Help:        "Total number of clients allow/deny-listed by postscreen (ALLOWLISTED/WHITELISTED/DENYLISTED/BLACKLISTED).",
				ConstLabels: constLabels,
			},
			[]string{"action"})
		e.postfixUp = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace:   "postfix",
				Subsystem:   "",
				Name:        "up",
				Help:        "Whether scraping Postfix's metrics was successful.",
				ConstLabels: constLabels,
			},
			[]string{"path"},
		)
	})
}

// NewPostfixExporter creates a new Postfix exporter instance. A nil
// logSrc collects showq metrics only.
func NewPostfixExporter(s *showq.Showq, logSrc logsource.LogSource, logUnsupportedLines bool, serviceLabels ...ServiceLabel) *PostfixExporter {
	postfixExporter := &PostfixExporter{
		cleanupLabels:       defaultCleanupLabels,
		lmtpLabels:          defaultLmtpLabels,
		pipeLabels:          defaultPipeLabels,
		qmgrLabels:          defaultQmgrLabels,
		smtpLabels:          defaultSmtpLabels,
		smtpdLabels:         defaultSmtpdLabels,
		bounceLabels:        defaultBounceLabels,
		virtualLabels:       defaultVirtualLabels,
		postscreenLabels:    defaultPostscreenLabels,
		instances:           defaultInstances,
		config:              &config.Config{},
		logUnsupportedLines: logUnsupportedLines,
		showq:               s,
		logSrc:              logSrc,
	}

	for _, serviceLabel := range serviceLabels {
		serviceLabel(postfixExporter)
	}

	postfixExporter.init()

	return postfixExporter
}

// Describe the Prometheus metrics that are going to be exported.
func (e *PostfixExporter) Describe(ch chan<- *prometheus.Desc) {
	e.postfixUp.Describe(ch)

	if e.logSrc == nil {
		return
	}
	ch <- e.cleanupProcesses.Desc()
	ch <- e.cleanupRejects.Desc()
	ch <- e.cleanupNotAccepted.Desc()
	e.cleanupActions.Describe(ch)
	e.lmtpDelays.Describe(ch)
	e.pipeDelays.Describe(ch)
	ch <- e.qmgrInsertsNrcptLegacy.Desc()
	ch <- e.qmgrInsertsNrcpt.Desc()
	ch <- e.qmgrInsertsSize.Desc()
	ch <- e.qmgrRemoves.Desc()
	ch <- e.qmgrExpires.Desc()
	e.qmgrStatuses.Describe(ch)
	e.smtpDelays.Describe(ch)
	e.smtpTLSConnects.Describe(ch)
	ch <- e.smtpDeferreds.Desc()
	e.smtpProcesses.Describe(ch)
	e.smtpDeferredDSN.Describe(ch)
	e.smtpBouncedDSN.Describe(ch)
	e.smtpReplies.Describe(ch)
	e.smtpdConnects.Describe(ch)
	e.smtpdDisconnects.Describe(ch)
	e.smtpdFCrDNSErrors.Describe(ch)
	e.smtpdLostConnections.Describe(ch)
	e.smtpdProcesses.Describe(ch)
	e.smtpdRejects.Describe(ch)
	e.smtpdSASLAuthenticationFailures.Describe(ch)
	e.smtpdTLSConnects.Describe(ch)
	e.accessActions.Describe(ch)
	e.messagesReceived.Describe(ch)
	e.bounceNotifications.Describe(ch)
	e.masterEvents.Describe(ch)
	e.masterProcessFailures.Describe(ch)
	e.masterThrottles.Describe(ch)
	e.deliveryStatuses.Describe(ch)
	e.deliveryDelays.Describe(ch)
	e.deliveryStageDelays.Describe(ch)
	e.deliveryStatusReplies.Describe(ch)
	ch <- e.smtpStatusDeferred.Desc()
	e.unsupportedLogEntries.Describe(ch)
	e.logEntries.Describe(ch)
	ch <- e.foreignLogEntries.Desc()
	e.smtpConnectionTimedOut.Describe(ch)
	e.smtpConnectionRefused.Describe(ch)
	ch <- e.bounceNonDelivery.Desc()
	ch <- e.bounceDelivery.Desc()
	ch <- e.virtualDelivered.Desc()
	ch <- e.postscreenConnects.Desc()
	e.postscreenConnectsRejected.Describe(ch)
	e.postscreenPasses.Describe(ch)
	ch <- e.postscreenDNSBLRank.Desc()
	e.postscreenRejects.Describe(ch)
	e.postscreenTests.Describe(ch)
	ch <- e.postscreenHangups.Desc()
	e.postscreenAccessList.Describe(ch)
}

// StartMetricCollection reads the log source until it fails or ctx is
// done, and returns the read error. It returns nil at once when the
// exporter has no log source.
func (e *PostfixExporter) StartMetricCollection(ctx context.Context) error {
	if e.logSrc == nil {
		return nil
	}

	gauge := e.postfixUp.WithLabelValues(e.logSrc.Path())
	defer gauge.Set(0)

	for {
		line, err := e.logSrc.Read(ctx)
		if err != nil {
			slog.Error("Couldn't read log source.", "source", e.logSrc.Path(), "error", err.Error())
			return err
		}
		e.CollectFromLogLine(line)
		gauge.Set(1)
	}
}

// Collect metrics from Postfix's showq and its log file.
func (e *PostfixExporter) Collect(ch chan<- prometheus.Metric) {
	if e.showq != nil {
		err := e.showq.Collect(ch)
		postfixUpGauge := e.postfixUp.WithLabelValues(e.showq.Path())
		if err == nil {
			postfixUpGauge.Set(1)
		} else {
			slog.Error("Failed to scrape showq", "error", err.Error())
			postfixUpGauge.Set(0)
		}
	}
	e.postfixUp.Collect(ch)

	if e.logSrc == nil {
		return
	}

	ch <- e.cleanupProcesses
	ch <- e.cleanupRejects
	ch <- e.cleanupNotAccepted
	e.cleanupActions.Collect(ch)
	e.lmtpDelays.Collect(ch)
	e.pipeDelays.Collect(ch)
	ch <- e.qmgrInsertsNrcptLegacy
	ch <- e.qmgrInsertsNrcpt
	ch <- e.qmgrInsertsSize
	ch <- e.qmgrRemoves
	ch <- e.qmgrExpires
	e.qmgrStatuses.Collect(ch)
	e.smtpDelays.Collect(ch)
	e.smtpTLSConnects.Collect(ch)
	ch <- e.smtpDeferreds
	e.smtpProcesses.Collect(ch)
	e.smtpReplies.Collect(ch)
	e.smtpdConnects.Collect(ch)
	e.smtpdDisconnects.Collect(ch)
	e.smtpdFCrDNSErrors.Collect(ch)
	e.smtpdLostConnections.Collect(ch)
	e.smtpdProcesses.Collect(ch)
	e.smtpDeferredDSN.Collect(ch)
	e.smtpBouncedDSN.Collect(ch)
	e.smtpdRejects.Collect(ch)
	e.smtpdSASLAuthenticationFailures.Collect(ch)
	e.smtpdTLSConnects.Collect(ch)
	e.accessActions.Collect(ch)
	e.messagesReceived.Collect(ch)
	e.bounceNotifications.Collect(ch)
	e.masterEvents.Collect(ch)
	e.masterProcessFailures.Collect(ch)
	e.masterThrottles.Collect(ch)
	e.deliveryStatuses.Collect(ch)
	e.deliveryDelays.Collect(ch)
	e.deliveryStageDelays.Collect(ch)
	e.deliveryStatusReplies.Collect(ch)
	ch <- e.smtpStatusDeferred
	e.unsupportedLogEntries.Collect(ch)
	e.logEntries.Collect(ch)
	ch <- e.foreignLogEntries
	e.smtpConnectionTimedOut.Collect(ch)
	e.smtpConnectionRefused.Collect(ch)
	ch <- e.bounceNonDelivery
	ch <- e.bounceDelivery
	ch <- e.virtualDelivered
	ch <- e.postscreenConnects
	e.postscreenConnectsRejected.Collect(ch)
	e.postscreenPasses.Collect(ch)
	ch <- e.postscreenDNSBLRank
	e.postscreenRejects.Collect(ch)
	e.postscreenTests.Collect(ch)
	ch <- e.postscreenHangups
	e.postscreenAccessList.Collect(ch)
}
