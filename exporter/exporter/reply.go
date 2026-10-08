// Reply parsing ported from github.com/sergeymakinen/postfix_exporter/v2,
// Copyright (c) 2022, Sergey Makinen, under the BSD 3-Clause License in
// LICENSE.sergeymakinen.

package exporter

import (
	"regexp"
	"strings"

	"github.com/patrickdk77/postfix_exporter/config"
)

var (
	reHostReplyStatus = regexp.MustCompile(`^(\d{3})(.{1,3}(\d\.\d{1,3}\.\d{1,3})|[^ ]+|) (.+)$`)
	reHostSaid        = regexp.MustCompile(`host \S+ said: (.+) \(in reply to \w+[\w /-]*\)`)
	reActionReply     = regexp.MustCompile(`^(?:(\d{3})[ -])?(?:(\d\.\d{1,3}\.\d{1,3}) )?(?:<[^>]*>: )?(.*)$`)
)

type hostReply struct {
	Code         string
	EnhancedCode string
	Text         string
}

func (r hostReply) value(match config.MatchType) string {
	switch match {
	case config.MatchTypeCode:
		return r.Code
	case config.MatchTypeEnhancedCode:
		return r.EnhancedCode
	default:
		return r.Text
	}
}

// parseHostReply splits a remote server reply such as
// "450 4.7.1 Try again later". enhancedCode fills in the enhanced code
// when the reply has none, for example from a status line's dsn=.
func parseHostReply(s, enhancedCode string) (reply hostReply) {
	if m := reHostSaid.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	if matches := reHostReplyStatus.FindStringSubmatch(s); matches != nil {
		reply.Code = matches[1]
		reply.EnhancedCode = matches[3]
		reply.Text = matches[4]
	} else {
		reply.Text = s
	}
	if reply.EnhancedCode == "" {
		reply.EnhancedCode = enhancedCode
	}
	return reply
}

// parseActionReply splits the reply on an access action line, such as
// "554 5.7.1 <a@example.com>: Relay access denied; from=<...> ...".
// The code and enhanced code are optional: discard and milter actions
// often log without them.
func parseActionReply(s string) hostReply {
	if i := strings.Index(s, "; from=<"); i >= 0 {
		s = s[:i]
	}
	m := reActionReply.FindStringSubmatch(s)
	return hostReply{Code: m[1], EnhancedCode: m[2], Text: m[3]}
}

func findSubmatch[S ~[]E, E any](slice S, f func(E) []int) (E, []int) {
	var zero E
	for _, e := range slice {
		if m := f(e); m != nil {
			return e, m
		}
	}
	return zero, nil
}

// replyText returns the text label of the first rule matching the
// reply, or "" when no rule matches.
func replyText(rules []config.ReplyMatchConfig, reply hostReply) string {
	rule, m := findSubmatch(rules, func(rule config.ReplyMatchConfig) []int {
		return rule.Regexp.FindStringSubmatchIndex(reply.value(rule.Match))
	})
	if m == nil {
		return ""
	}
	return string(rule.Regexp.ExpandString(nil, rule.Text, reply.value(rule.Match), m))
}

// statusReplyText is replyText for delivery status lines, where rules
// can be limited to some statuses.
func statusReplyText(rules []config.StatusReplyMatchConfig, status string, reply hostReply) string {
	rule, m := findSubmatch(rules, func(rule config.StatusReplyMatchConfig) []int {
		if !rule.AppliesTo(status) {
			return nil
		}
		return rule.Regexp.FindStringSubmatchIndex(reply.value(rule.Match))
	})
	if m == nil {
		return ""
	}
	return string(rule.Regexp.ExpandString(nil, rule.Text, reply.value(rule.Match), m))
}
