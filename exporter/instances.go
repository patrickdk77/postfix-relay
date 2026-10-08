package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/patrickdk77/postfix_exporter/exporter"
)

// resolveInstances turns --postfix.instance values into instances.
// "auto" asks postconf for main.cf's expanded syslog_name and every
// master.cf -o syslog_name override.
func resolveInstances(specs []string, postconf string) ([]exporter.Instance, error) {
	var instances []exporter.Instance
	for _, spec := range specs {
		if spec != "auto" {
			instances = append(instances, exporter.ParseInstance(spec))
			continue
		}
		auto, err := postconfInstances(postconf)
		if err != nil {
			return nil, err
		}
		instances = append(instances, auto...)
	}
	return instances, nil
}

func postconfInstances(postconf string) ([]exporter.Instance, error) {
	// -x expands the default, ${multi_instance_name?...:{postfix}}.
	out, err := exec.Command(postconf, "-xh", "syslog_name").Output()
	if err != nil {
		return nil, fmt.Errorf("running %s -xh syslog_name: %w", postconf, err)
	}
	name := strings.TrimSpace(string(out))
	if name == "" {
		return nil, errors.New(postconf + " -xh syslog_name returned nothing")
	}
	instances := []exporter.Instance{{SyslogName: name}}

	out, err = exec.Command(postconf, "-xP").Output()
	if err != nil {
		return nil, fmt.Errorf("running %s -xP: %w", postconf, err)
	}
	return append(instances, parseServiceSyslogNames(string(out))...), nil
}

// parseServiceSyslogNames reads postconf -xP lines such as
// "relay/unix/syslog_name = postfix/relay".
func parseServiceSyslogNames(out string) []exporter.Instance {
	var instances []exporter.Instance
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, " = ")
		if !ok || value == "" {
			continue
		}
		serviceType, ok := strings.CutSuffix(key, "/syslog_name")
		if !ok {
			continue
		}
		i := strings.LastIndexByte(serviceType, '/')
		if i <= 0 {
			continue
		}
		instances = append(instances, exporter.Instance{SyslogName: strings.TrimSpace(value), Service: serviceType[:i]})
	}
	return instances
}
