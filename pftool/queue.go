package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type recipient struct {
	Address     string `json:"address"`
	DelayReason string `json:"delay_reason"`
}

type queueEntry struct {
	QueueName   string      `json:"queue_name"`
	QueueID     string      `json:"queue_id"`
	ArrivalTime int64       `json:"arrival_time"`
	MessageSize int64       `json:"message_size"`
	Sender      string      `json:"sender"`
	Recipients  []recipient `json:"recipients"`
}

func (e queueEntry) held() bool { return e.QueueName == "hold" }

// matchesAddress reports whether key occurs anywhere in addr, ignoring
// case. Sender addresses are often random, so a fragment such as a domain
// or part of a local part has to match.
func matchesAddress(addr, key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return false
	}
	return strings.Contains(strings.ToLower(addr), key)
}

func (e queueEntry) matches(key string) bool {
	if matchesAddress(e.Sender, key) {
		return true
	}
	for _, r := range e.Recipients {
		if matchesAddress(r.Address, key) {
			return true
		}
	}
	return false
}

// postfixCommand returns the path of a Postfix command. PFTOOL_BIN_DIR
// overrides the lookup so tests can run against stubs.
func postfixCommand(name string) string {
	if dir := os.Getenv("PFTOOL_BIN_DIR"); dir != "" {
		return filepath.Join(dir, name)
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return filepath.Join("/usr/sbin", name)
}

func parseQueue(r io.Reader) ([]queueEntry, error) {
	var entries []queueEntry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	line := 0
	for sc.Scan() {
		line++
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var e queueEntry
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, fmt.Errorf("postqueue -j line %d: %w", line, err)
		}
		if e.QueueID == "" {
			return nil, fmt.Errorf("postqueue -j line %d: missing queue_id", line)
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("postqueue -j: %w", err)
	}
	return entries, nil
}

func listQueue() ([]queueEntry, error) {
	cmd := exec.Command(postfixCommand("postqueue"), "-j")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("postqueue -j: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseQueue(bytes.NewReader(out))
}

// selectIDs returns the queue IDs of entries matching key for which keep
// returns true, in listing order and without duplicates. A negative max
// means no limit.
func selectIDs(entries []queueEntry, key string, max int, keep func(queueEntry) bool) []string {
	var ids []string
	seen := map[string]bool{}
	for _, e := range entries {
		if max >= 0 && len(ids) >= max {
			break
		}
		if seen[e.QueueID] || !keep(e) || !e.matches(key) {
			continue
		}
		seen[e.QueueID] = true
		ids = append(ids, e.QueueID)
	}
	return ids
}

func selectHold(entries []queueEntry, key string) []string {
	return selectIDs(entries, key, -1, func(e queueEntry) bool { return !e.held() })
}

func selectRelease(entries []queueEntry, key string, max int) []string {
	return selectIDs(entries, key, max, func(e queueEntry) bool { return e.held() })
}

func selectDelete(entries []queueEntry, key string) []string {
	return selectIDs(entries, key, -1, func(queueEntry) bool { return true })
}

// postsuper runs "postsuper <flag> -" with ids on stdin. Nothing runs for an
// empty list.
func postsuper(flag string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	cmd := exec.Command(postfixCommand("postsuper"), flag, "-")
	cmd.Stdin = strings.NewReader(strings.Join(ids, "\n") + "\n")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if msg := strings.TrimSpace(out.String()); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	if err != nil {
		return fmt.Errorf("postsuper %s: %v", flag, err)
	}
	return nil
}

func postcat(queueID string) ([]byte, error) {
	cmd := exec.Command(postfixCommand("postcat"), "-q", queueID)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("postcat -q %s: %v: %s", queueID, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
