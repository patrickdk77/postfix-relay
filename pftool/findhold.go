package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"fmt"
	"log"
	"math/big"
	"mime"
	"mime/multipart"
	"net/smtp"
	"net/textproto"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
)

const (
	statusPending  = 0
	statusSent     = 1
	statusDeleted  = 2
	statusAccepted = 3
)

type findHoldConfig struct {
	dsn        string
	mailServer string
	alertFrom  string
	alertTo    string
	portalURL  string
	releaseMax int
	recheck    time.Duration
	sendEmail  bool
	lockFile   string
	interval   time.Duration
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadFindHoldConfig() (findHoldConfig, error) {
	godotenv.Load(getenv("FIND_HOLD_CONFIG", "/etc/find_hold.conf"))

	var cfg findHoldConfig
	host, name, user, pass := os.Getenv("DBHOST"), os.Getenv("DBNAME"), os.Getenv("DBUSER"), os.Getenv("DBPASS")
	if host == "" || name == "" || user == "" {
		return cfg, fmt.Errorf("DBHOST, DBNAME, DBUSER and DBPASS must be set")
	}
	if !strings.Contains(host, ":") {
		host += ":" + getenv("DBPORT", "3306")
	}
	cfg.dsn = fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true&timeout=10s", user, pass, host, name)

	cfg.mailServer = getenv("MAIL_SERVER", "localhost:25")
	if !strings.Contains(cfg.mailServer, ":") {
		cfg.mailServer += ":25"
	}
	cfg.alertFrom = os.Getenv("ALERT_FROM")
	cfg.alertTo = os.Getenv("ALERT_TO")
	cfg.portalURL = strings.TrimRight(getenv("PORTAL_URL", "https://example.com/queue"), "/")
	cfg.lockFile = getenv("LOCK_FILE", "/run/find_hold.lock")

	var err error
	if cfg.releaseMax, err = strconv.Atoi(getenv("RELEASE_MAX", "75")); err != nil || cfg.releaseMax < 0 {
		return cfg, fmt.Errorf("RELEASE_MAX must be a non-negative integer")
	}
	mins, err := strconv.Atoi(getenv("HOLD_RECHECK_MINUTES", "45"))
	if err != nil || mins < 0 {
		return cfg, fmt.Errorf("HOLD_RECHECK_MINUTES must be a non-negative integer")
	}
	cfg.recheck = time.Duration(mins) * time.Minute
	if cfg.sendEmail, err = strconv.ParseBool(getenv("SEND_EMAIL", "true")); err != nil {
		return cfg, fmt.Errorf("SEND_EMAIL must be true or false")
	}
	if cfg.sendEmail && (cfg.alertFrom == "" || cfg.alertTo == "") {
		return cfg, fmt.Errorf("ALERT_FROM and ALERT_TO must be set when SEND_EMAIL is true")
	}
	if v := os.Getenv("FIND_HOLD_INTERVAL"); v != "" {
		if cfg.interval, err = time.ParseDuration(v); err != nil || cfg.interval <= 0 {
			return cfg, fmt.Errorf("FIND_HOLD_INTERVAL must be a positive duration such as 60s")
		}
	}
	return cfg, nil
}

func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("already running (%s is locked)", path)
	}
	f.Truncate(0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return f, nil
}

func cmdFindHold(args []string) error {
	if len(args) != 0 {
		return errUsage
	}
	cfg, err := loadFindHoldConfig()
	if err != nil {
		return err
	}
	if err := requireRoot(); err != nil {
		return err
	}
	lock, err := acquireLock(cfg.lockFile)
	if err != nil {
		return err
	}
	defer lock.Close()

	db, err := sql.Open("mysql", cfg.dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetConnMaxLifetime(5 * time.Minute)

	for {
		if err := runFindHold(db, cfg); err != nil {
			if cfg.interval == 0 {
				return err
			}
			log.Printf("find_hold: %v", err)
		}
		if cfg.interval == 0 {
			return nil
		}
		time.Sleep(cfg.interval)
	}
}

type policyRow struct {
	status int
	policy string
	hash   string
}

func queryPolicy(db *sql.DB, sender string) (*policyRow, error) {
	var row policyRow
	var policy, hash sql.NullString
	err := db.QueryRow(`SELECT status, policy, hash FROM mail_sender_policy WHERE sender=? LIMIT 1`, sender).
		Scan(&row.status, &policy, &hash)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("mail_sender_policy lookup for %s: %w", sender, err)
	}
	row.policy, row.hash = policy.String, hash.String
	return &row, nil
}

// lookupPolicy finds the policy row for a sender, falling back to a row for
// the sender's domain. It returns the row and the key the row is stored under.
func lookupPolicy(db *sql.DB, sender string) (*policyRow, string, error) {
	row, err := queryPolicy(db, sender)
	if err != nil || row != nil {
		return row, sender, err
	}
	at := strings.LastIndex(sender, "@")
	if at < 0 || at == len(sender)-1 {
		return nil, "", nil
	}
	domain := sender[at+1:]
	row, err = queryPolicy(db, domain)
	if err != nil || row == nil {
		return nil, "", err
	}
	return row, domain, nil
}

func holdSenders(db *sql.DB, recheck time.Duration) ([]string, error) {
	rows, err := db.Query(`SELECT sender FROM mail_sender_policy WHERE policy='HOLD'
		AND (status=0 OR (status=1 AND lastupdate > DATE_SUB(NOW(), INTERVAL ? MINUTE)))`,
		int(recheck.Minutes()))
	if err != nil {
		return nil, fmt.Errorf("mail_sender_policy HOLD query: %w", err)
	}
	defer rows.Close()
	var senders []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		senders = append(senders, s)
	}
	return senders, rows.Err()
}

type heldSender struct {
	count   int
	queueID string
}

func heldBySender(entries []queueEntry) map[string]*heldSender {
	held := map[string]*heldSender{}
	for _, e := range entries {
		if !e.held() {
			continue
		}
		h := held[e.Sender]
		if h == nil {
			h = &heldSender{}
			held[e.Sender] = h
		}
		h.count++
		h.queueID = e.QueueID
	}
	return held
}

// without drops entries whose queue ID is in done.
func without(entries []queueEntry, done map[string]bool) []queueEntry {
	if len(done) == 0 {
		return entries
	}
	var out []queueEntry
	for _, e := range entries {
		if !done[e.QueueID] {
			out = append(out, e)
		}
	}
	return out
}

func runFindHold(db *sql.DB, cfg findHoldConfig) error {
	entries, err := listQueue()
	if err != nil {
		return err
	}

	senders, err := holdSenders(db, cfg.recheck)
	if err != nil {
		return err
	}
	heldAny := false
	for _, s := range senders {
		ids := selectHold(entries, s)
		if len(ids) == 0 {
			continue
		}
		if err := postsuper("-h", ids); err != nil {
			return err
		}
		log.Printf("find_hold: held %d message(s) for %s", len(ids), s)
		heldAny = true
	}
	if heldAny {
		if entries, err = listQueue(); err != nil {
			return err
		}
	}

	held := heldBySender(entries)
	order := make([]string, 0, len(held))
	for s := range held {
		order = append(order, s)
	}
	sort.Strings(order)

	done := map[string]bool{}
	for _, sender := range order {
		info := held[sender]
		fmt.Printf("%d\t%s\n", info.count, sender)
		row, key, err := lookupPolicy(db, sender)
		if err != nil {
			return err
		}
		if row == nil {
			continue
		}
		switch row.status {
		case statusPending:
			if err := alertAndMark(db, cfg, sender, key, info, row.hash); err != nil {
				log.Printf("find_hold: alert for %s: %v", sender, err)
			}
		case statusDeleted:
			ids := selectDelete(without(entries, done), key)
			if err := postsuper("-d", ids); err != nil {
				return err
			}
			for _, id := range ids {
				done[id] = true
			}
			log.Printf("find_hold: deleted %d message(s) for %s", len(ids), key)
		case statusAccepted:
			ids := selectRelease(without(entries, done), key, cfg.releaseMax)
			if err := postsuper("-H", ids); err != nil {
				return err
			}
			for _, id := range ids {
				done[id] = true
			}
			log.Printf("find_hold: released %d message(s) for %s", len(ids), key)
		}
	}
	return nil
}

var (
	postcatSkip   = regexp.MustCompile(`^(named_attribute|warning_message|original_recipient)`)
	postcatMarker = regexp.MustCompile(`\*\*\* MESSAGE CONTENTS [^*]+\*\*\*`)
	authSender    = regexp.MustCompile(`\(Authenticated sender: ([^)\n]*)\)`)
)

// splitPostcat drops the envelope records the old script filtered out and
// splits postcat output into the envelope summary and the message itself.
func splitPostcat(raw []byte) (envelope, body string) {
	var kept []string
	for _, l := range strings.Split(string(raw), "\n") {
		if postcatSkip.MatchString(l) {
			continue
		}
		kept = append(kept, l)
	}
	text := strings.Join(kept, "\n")
	loc := postcatMarker.FindStringIndex(text)
	if loc == nil {
		return text, ""
	}
	return text[:loc[0]], strings.TrimLeft(text[loc[1]:], "\n")
}

func extractAccount(body string) string {
	if m := authSender.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

const hashAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-"

func newHash(n int) (string, error) {
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(hashAlphabet))))
		if err != nil {
			return "", err
		}
		b[i] = hashAlphabet[idx.Int64()]
	}
	return string(b), nil
}

func encodeHeader(s string) string {
	for _, c := range s {
		if c > 126 || c < 32 {
			return mime.QEncoding.Encode("utf-8", s)
		}
	}
	return s
}

// buildAlert composes the admin alert: a text part with the portal links and
// the envelope summary, and the held message attached as message/rfc822.
func buildAlert(from, to, subject, text, message string) ([]byte, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fmt.Fprintf(&buf, "From: %s\nTo: %s\nSubject: %s\nDate: %s\nMIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=%q\n\n",
		from, to, encodeHeader(subject), time.Now().Format(time.RFC1123Z), mw.Boundary())
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Type", "text/plain; charset=utf-8")
	hdr.Set("Content-Transfer-Encoding", "8bit")
	pw, err := mw.CreatePart(hdr)
	if err != nil {
		return nil, err
	}
	if _, err := pw.Write([]byte(text)); err != nil {
		return nil, err
	}
	hdr = textproto.MIMEHeader{}
	hdr.Set("Content-Type", "message/rfc822")
	pw, err = mw.CreatePart(hdr)
	if err != nil {
		return nil, err
	}
	if _, err := pw.Write([]byte(message)); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func alertAndMark(db *sql.DB, cfg findHoldConfig, sender, key string, info *heldSender, hash string) error {
	raw, err := postcat(info.queueID)
	if err != nil {
		return err
	}
	envelope, body := splitPostcat(raw)
	if strings.TrimSpace(envelope) == "" {
		return nil
	}
	if !cfg.sendEmail {
		return nil
	}
	if hash == "" {
		if hash, err = newHash(32); err != nil {
			return err
		}
	}
	account := extractAccount(body)
	text := fmt.Sprintf("%s/%s/delete/%s\n\n%s/%s/accept\n\n%s", cfg.portalURL, hash, account, cfg.portalURL, hash, envelope)
	subject := fmt.Sprintf("Spam: %d, %s, %s", info.count, sender, key)
	msg, err := buildAlert(cfg.alertFrom, cfg.alertTo, subject, text, body)
	if err != nil {
		return err
	}
	if err := smtp.SendMail(cfg.mailServer, nil, cfg.alertFrom, []string{cfg.alertTo}, msg); err != nil {
		return fmt.Errorf("sending alert via %s: %w", cfg.mailServer, err)
	}
	if _, err := db.Exec(`UPDATE mail_sender_policy SET status=1, hash=?, lastupdate=NOW() WHERE sender=?`, hash, key); err != nil {
		return fmt.Errorf("marking %s as alerted: %w", key, err)
	}
	log.Printf("find_hold: alert sent for %s (%d held), hash %s", sender, info.count, hash)
	return nil
}
