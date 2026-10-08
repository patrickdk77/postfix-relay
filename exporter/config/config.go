// Package config loads the optional YAML file that turns Postfix reply
// texts into bounded label values.
//
// Ported from github.com/sergeymakinen/postfix_exporter/v2/config,
// Copyright (c) 2022, Sergey Makinen, under the BSD 3-Clause License
// in LICENSE.sergeymakinen.
package config

import (
	"errors"
	"os"
	"regexp"
	"slices"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config holds the reply matching rules. Each list is tried in order
// and the first matching rule sets the text label.
type Config struct {
	StatusReplies []StatusReplyMatchConfig `yaml:"status_replies,omitempty"`
	SmtpReplies   []ReplyMatchConfig       `yaml:"smtp_replies,omitempty"`
	RejectReplies []ReplyMatchConfig       `yaml:"reject_replies,omitempty"`
}

// Load reads and validates a config file.
func Load(name string) (*Config, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, errors.New("error reading config file: " + err.Error())
	}
	defer f.Close()
	d := yaml.NewDecoder(f)
	d.KnownFields(true)
	var cfg Config
	if err = d.Decode(&cfg); err != nil {
		return nil, errors.New("error parsing config file: " + err.Error())
	}
	return &cfg, nil
}

// StatusReplyMatchConfig matches the reply on a delivery status line.
type StatusReplyMatchConfig struct {
	Statuses    []string  `yaml:"statuses,omitempty"`
	NotStatuses []string  `yaml:"not_statuses,omitempty"`
	Regexp      *Regexp   `yaml:"regexp"`
	Match       MatchType `yaml:"match,omitempty"`
	Text        string    `yaml:"text"`
}

func (cfg *StatusReplyMatchConfig) UnmarshalYAML(value *yaml.Node) error {
	type plain StatusReplyMatchConfig
	if err := value.Decode((*plain)(cfg)); err != nil {
		return err
	}
	if cfg.Regexp == nil {
		return errors.New("missing regexp")
	}
	if cfg.Text == "" {
		return errors.New("empty text replacement")
	}
	return nil
}

// AppliesTo reports whether the rule covers a delivery status.
func (cfg StatusReplyMatchConfig) AppliesTo(status string) bool {
	if len(cfg.Statuses) > 0 && !slices.Contains(cfg.Statuses, status) {
		return false
	}
	return !slices.Contains(cfg.NotStatuses, status)
}

// ReplyMatchConfig matches an SMTP reply.
type ReplyMatchConfig struct {
	Regexp *Regexp   `yaml:"regexp"`
	Match  MatchType `yaml:"match,omitempty"`
	Text   string    `yaml:"text"`
}

func (cfg *ReplyMatchConfig) UnmarshalYAML(value *yaml.Node) error {
	type plain ReplyMatchConfig
	if err := value.Decode((*plain)(cfg)); err != nil {
		return err
	}
	if cfg.Regexp == nil {
		return errors.New("missing regexp")
	}
	if cfg.Text == "" {
		return errors.New("empty text replacement")
	}
	return nil
}

// MatchType selects which part of a reply a rule's regexp runs on.
type MatchType int

func (t *MatchType) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	switch s {
	case "", "text":
		*t = MatchTypeText
	case "code":
		*t = MatchTypeCode
	case "enhanced_code":
		*t = MatchTypeEnhancedCode
	default:
		return errors.New("unsupported match type " + strconv.Quote(s))
	}
	return nil
}

// MatchType types.
const (
	MatchTypeText MatchType = iota
	MatchTypeCode
	MatchTypeEnhancedCode
)

// Regexp is a regexp.Regexp that unmarshals from a YAML string.
type Regexp struct {
	*regexp.Regexp
}

func (r *Regexp) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	re, err := regexp.Compile(s)
	if err != nil {
		return err
	}
	*r = Regexp{re}
	return nil
}
