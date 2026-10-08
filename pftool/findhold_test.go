package main

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"
)

const postcatSample = `*** ENVELOPE RECORDS deferred/A/AAA111 ***
message_size:            1200             640               1               0            1200               0                       0
message_arrival_time: Tue Oct  7 00:00:00 2026
create_time: Tue Oct  7 00:00:00 2026
named_attribute: log_ident=AAA111
named_attribute: rewrite_context=local
sender_fullname: Alice
sender: alice@spam.example
named_attribute: log_client_name=unknown
original_recipient: bob@remote.example
recipient: bob@remote.example
warning_message: something
*** MESSAGE CONTENTS deferred/A/AAA111 ***
Received: from laptop (unknown [203.0.113.5])
	(Authenticated sender: alice@spam.example)
	by mail.example (Postfix) with ESMTPSA id AAA111
Subject: buy now
From: alice@spam.example

body text
named_attribute: inside body stays? no, the old filter dropped it too
*** HEADER EXTRACTED deferred/A/AAA111 ***
*** MESSAGE FILE END deferred/A/AAA111 ***
`

func TestSplitPostcat(t *testing.T) {
	envelope, body := splitPostcat([]byte(postcatSample))
	for _, bad := range []string{"named_attribute", "warning_message", "original_recipient", "MESSAGE CONTENTS"} {
		if strings.Contains(envelope, bad) || strings.Contains(body, bad) {
			t.Errorf("%q must be filtered, envelope=%q body=%q", bad, envelope, body)
		}
	}
	if !strings.Contains(envelope, "sender: alice@spam.example") || !strings.Contains(envelope, "recipient: bob@remote.example") {
		t.Errorf("envelope = %q", envelope)
	}
	if !strings.HasPrefix(body, "Received: from laptop") || !strings.Contains(body, "body text") {
		t.Errorf("body = %q", body)
	}
	if got := extractAccount(body); got != "alice@spam.example" {
		t.Errorf("account = %q", got)
	}
}

func TestSplitPostcatWithoutMarker(t *testing.T) {
	envelope, body := splitPostcat([]byte("sender: a@b\nrecipient: c@d\n"))
	if body != "" || !strings.Contains(envelope, "sender: a@b") {
		t.Errorf("envelope=%q body=%q", envelope, body)
	}
	if extractAccount("no auth header here") != "" {
		t.Error("account must be empty without an Authenticated sender line")
	}
}

func TestBuildAlert(t *testing.T) {
	raw, err := buildAlert("alert@x.example", "admin@x.example", "Spam: 3, alice@spam.example, spam.example",
		"https://portal/queue/HASH/delete/alice@spam.example\n\nhttps://portal/queue/HASH/accept\n\nsender: alice@spam.example\n",
		"Subject: buy now\n\nbody text\n")
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("alert is not a parseable message: %v\n%s", err, raw)
	}
	if msg.Header.Get("Subject") != "Spam: 3, alice@spam.example, spam.example" || msg.Header.Get("To") != "admin@x.example" {
		t.Errorf("headers: %v", msg.Header)
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/mixed" {
		t.Fatalf("content type %q: %v", msg.Header.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var types []string
	var bodies []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		types = append(types, p.Header.Get("Content-Type"))
		bodies = append(bodies, string(b))
	}
	if len(types) != 2 || !strings.HasPrefix(types[0], "text/plain") || types[1] != "message/rfc822" {
		t.Fatalf("parts = %v", types)
	}
	if !strings.Contains(bodies[0], "/queue/HASH/accept") || !strings.Contains(bodies[0], "sender: alice@spam.example") {
		t.Errorf("text part = %q", bodies[0])
	}
	if !strings.Contains(bodies[1], "Subject: buy now") {
		t.Errorf("rfc822 part = %q", bodies[1])
	}
}

func TestEncodeHeader(t *testing.T) {
	if encodeHeader("plain subject") != "plain subject" {
		t.Error("ASCII must stay as is")
	}
	if enc := encodeHeader("café"); !strings.HasPrefix(enc, "=?utf-8?q?") {
		t.Errorf("non-ASCII must be Q-encoded, got %q", enc)
	}
}

func TestNewHash(t *testing.T) {
	h, err := newHash(32)
	if err != nil || len(h) != 32 {
		t.Fatalf("hash %q err %v", h, err)
	}
	for _, c := range h {
		if !strings.ContainsRune(hashAlphabet, c) {
			t.Fatalf("hash %q has %q outside the alphabet", h, c)
		}
	}
	h2, _ := newHash(32)
	if h == h2 {
		t.Fatal("two hashes identical")
	}
}

func TestHeldBySender(t *testing.T) {
	entries := loadFixture(t)
	held := heldBySender(entries)
	if len(held) != 3 {
		t.Fatalf("held senders = %v", held)
	}
	if h := held["alice@spam.example"]; h == nil || h.count != 1 || h.queueID != "BBB222" {
		t.Errorf("alice = %+v", h)
	}
	if h := held["xalice@spam.example"]; h == nil || h.count != 2 {
		t.Errorf("duplicate listing of one held message counts twice as the old script did: %+v", h)
	}
	if held["dave@other.example"] != nil {
		t.Error("active messages are not held")
	}
	rest := without(entries, map[string]bool{"AAA111": true, "BBB222": true})
	if len(rest) != 4 {
		t.Errorf("without = %d entries", len(rest))
	}
}

func setBaseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FIND_HOLD_CONFIG", "/nonexistent/find_hold.conf")
	for _, k := range []string{"DBHOST", "DBPORT", "DBNAME", "DBUSER", "DBPASS", "MAIL_SERVER", "ALERT_FROM", "ALERT_TO",
		"PORTAL_URL", "RELEASE_MAX", "HOLD_RECHECK_MINUTES", "SEND_EMAIL", "LOCK_FILE", "FIND_HOLD_INTERVAL"} {
		t.Setenv(k, "")
	}
	t.Setenv("DBHOST", "db.example")
	t.Setenv("DBNAME", "ysmaster")
	t.Setenv("DBUSER", "u")
	t.Setenv("DBPASS", "p")
	t.Setenv("ALERT_FROM", "alert@x.example")
	t.Setenv("ALERT_TO", "admin@x.example")
}

func TestLoadFindHoldConfigDefaults(t *testing.T) {
	setBaseEnv(t)
	cfg, err := loadFindHoldConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.dsn != "u:p@tcp(db.example:3306)/ysmaster?parseTime=true&timeout=10s" {
		t.Errorf("dsn = %q", cfg.dsn)
	}
	if cfg.mailServer != "localhost:25" || cfg.releaseMax != 75 || cfg.recheck != 45*time.Minute ||
		!cfg.sendEmail || cfg.lockFile != "/run/find_hold.lock" || cfg.interval != 0 ||
		cfg.portalURL != "https://example.com/queue" {
		t.Errorf("defaults wrong: %+v", cfg)
	}
}

func TestLoadFindHoldConfigOverrides(t *testing.T) {
	setBaseEnv(t)
	t.Setenv("DBHOST", "db.example:6033")
	t.Setenv("MAIL_SERVER", "smtp.example")
	t.Setenv("PORTAL_URL", "https://portal.example/queue/")
	t.Setenv("RELEASE_MAX", "10")
	t.Setenv("HOLD_RECHECK_MINUTES", "5")
	t.Setenv("SEND_EMAIL", "false")
	t.Setenv("FIND_HOLD_INTERVAL", "90s")
	cfg, err := loadFindHoldConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg.dsn, "@tcp(db.example:6033)/") || cfg.mailServer != "smtp.example:25" ||
		cfg.portalURL != "https://portal.example/queue" || cfg.releaseMax != 10 ||
		cfg.recheck != 5*time.Minute || cfg.sendEmail || cfg.interval != 90*time.Second {
		t.Errorf("overrides wrong: %+v", cfg)
	}
}

func TestLoadFindHoldConfigErrors(t *testing.T) {
	cases := map[string]func(*testing.T){
		"no db":                func(t *testing.T) { t.Setenv("DBHOST", "") },
		"bad release max":      func(t *testing.T) { t.Setenv("RELEASE_MAX", "lots") },
		"negative release max": func(t *testing.T) { t.Setenv("RELEASE_MAX", "-1") },
		"bad recheck":          func(t *testing.T) { t.Setenv("HOLD_RECHECK_MINUTES", "soon") },
		"bad send email":       func(t *testing.T) { t.Setenv("SEND_EMAIL", "maybe") },
		"missing alert to":     func(t *testing.T) { t.Setenv("ALERT_TO", "") },
		"bad interval":         func(t *testing.T) { t.Setenv("FIND_HOLD_INTERVAL", "often") },
		"zero interval":        func(t *testing.T) { t.Setenv("FIND_HOLD_INTERVAL", "0s") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			setBaseEnv(t)
			mutate(t)
			if _, err := loadFindHoldConfig(); err == nil {
				t.Errorf("%s: config loaded without error", name)
			}
		})
	}
}

func TestAcquireLock(t *testing.T) {
	path := t.TempDir() + "/lock"
	f, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := acquireLock(path); err == nil {
		t.Fatal("second lock must fail while the first is held")
	}
}
