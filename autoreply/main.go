package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/mail"
	"net/smtp"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
	"github.com/valkey-io/valkey-go"
)

var (
	DEBUG                    bool
	MAX_MESSAGE_SIZE_TO_READ int
	MAIL_SERVER              string
	DBNAME                   string
	DBHOST                   string
	DBUSERNAME               string
	DBPASS                   string
	VALKEY_URL               string
)

const (
	DEFAULT_SUBJECT  = "AutoResponse  ({S})"
	DEFAULT_RESPONSE = "Your message entitled \"{S}\" was received on {D}.\r\nThank you\r\n"
)

func mylog(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	// Following Perl script's log format: %m %d %H:%M:%S : [$SCRIPT_NAME]: ...
	fmt.Printf("%s: [%s]: %s\n", time.Now().Format("01 02 15:04:05"), os.Args[0], msg)
}

func parseEnvVars() {
	// Load from config file. Existing environment variables will take precedence.
	godotenv.Load("/etc/autoreply.conf")

	DEBUG, _ = strconv.ParseBool(getEnvOrDefault("DEBUG", "false"))
	MAX_MESSAGE_SIZE_TO_READ, _ = strconv.Atoi(getEnvOrDefault("SIZE_TO_READ", "10000"))
	MAIL_SERVER = getEnvOrDefault("MAIL_SERVER", "localhost")
	DBNAME = os.Getenv("DBNAME")
	DBHOST = os.Getenv("DBHOST")
	DBUSERNAME = os.Getenv("DBUSER")
	DBPASS = os.Getenv("DBPASS")
	VALKEY_URL = getEnvOrDefault("VALKEY_URL", "valkey://localhost:6379/0")
}

func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// valkeyKey returns the Valkey key for a given (recipient, sender) pair.
func valkeyKey(myemailaddress, sender string) string {
	return fmt.Sprintf("autoreply:%s:%s", myemailaddress, sender)
}

func main() {
	parseEnvVars()
	if len(os.Args) != 4 {
		fmt.Fprintf(os.Stderr, "\nUSAGE: `cat email_text` | %s username recipient_emailaddress sender_emailaddress\n", os.Args[0])
		os.Exit(1)
	}

	user := os.Args[1]
	recipient := strings.TrimSuffix(os.Args[2], ".autoreply")
	sender := os.Args[3]

	parts := strings.SplitN(recipient, "@", 2)
	var recipientUser, domain string
	if len(parts) == 2 {
		recipientUser = parts[0]
		domain = parts[1]
	} else {
		recipientUser = recipient
	}

	myemailaddress := fmt.Sprintf("%s@%s", recipientUser, domain)
	reply := true
	autoresponseFrom := "Autoreply Daemon <AUTOREPLY_DAEMON@"

	if DEBUG {
		mylog("ARGV: %s, %s, %s", user, recipient, sender)
	}

	mylog("user = [%s], sender = [%s] ", user, sender)

	// Read from Stdin up to MAX_MESSAGE_SIZE_TO_READ
	inputBytes, err := io.ReadAll(io.LimitReader(os.Stdin, int64(MAX_MESSAGE_SIZE_TO_READ)))
	if err != nil {
		mylog("Error reading stdin: %v", err)
		return
	}

	// replace \r?\n with \r\n
	re := regexp.MustCompile(`\r?\n`)
	msgtxt := re.ReplaceAllString(string(inputBytes), "\r\n")

	msg, err := mail.ReadMessage(strings.NewReader(msgtxt))
	if err != nil {
		mylog("Error parsing message: %v", err)
		return
	}

	// Connect to MySQL (reads only - mail_accounts config)
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s", DBUSERNAME, DBPASS, DBHOST, DBNAME)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		mylog("Could not connect to MySQL: %v", err)
		os.Exit(1)
	}
	defer db.Close()

	// Connect to Valkey
	valkeyOpt, err := valkey.ParseURL(VALKEY_URL)
	if err != nil {
		mylog("Invalid VALKEY_URL %q: %v", VALKEY_URL, err)
		os.Exit(1)
	}
	vc, err := valkey.NewClient(valkeyOpt)
	if err != nil {
		mylog("Could not connect to Valkey: %v", err)
		os.Exit(1)
	}
	defer vc.Close()

	minElapsed, newSubject, responseText := getConfigFromDB(db, myemailaddress)
	lastReplied := getLastRepliedFromValkey(vc, myemailaddress, sender)

	if reply && strings.HasPrefix(strings.ToLower(sender), strings.ToLower(autoresponseFrom)) {
		reply = false
		mylog("Skipping reply to myself [%s].", sender)
	}

	if reply && regexp.MustCompile(`(?i)MAILER\-DAEMON`).MatchString(sender) {
		reply = false
		mylog("Skipping reply to [%s], assuming it is from user CGI's on callisto/europa", sender)
	}

	if reply && regexp.MustCompile(`(?i)YES`).MatchString(msg.Header.Get("X-Spam-Flag")) {
		reply = false
		mylog("Skipping reply to SPAM: [%s].", sender)
	}

	if reply && strings.EqualFold(sender, myemailaddress) {
		reply = false
		mylog("Skipping reply to myself: [%s].", sender)
	}

	if reply && lastReplied != 0 {
		elapsedTime := time.Now().Unix() - lastReplied
		if elapsedTime <= minElapsed {
			reply = false
			mylog("Skipping reply to [%s], elapsed time since last reply was %d seconds.", sender, elapsedTime)
		}
	}

	if reply {
		toCc := msg.Header.Get("To") + msg.Header.Get("Cc")
		if !regexp.MustCompile(`(?i)`+regexp.QuoteMeta(domain)).MatchString(toCc) {
			reply = false
			mylog("Skipping reply to [%s]. [%s] not found in TO or CC", sender, domain)
			if DEBUG {
				mylog("--- sender = [%s], recipient=[%s], to_cc=[%s]", sender, myemailaddress, toCc)
			}
		}
	}

	if reply {
		senderMatch := regexp.MustCompile(`(?i)(^bounce|owner-|-owner|-request)`).MatchString(sender)
		precMatch := regexp.MustCompile(`(?i)(list|bulk|junk)`).MatchString(msg.Header.Get("Precedence"))
		autoSubMatch := regexp.MustCompile(`(?i)(Yes|True)`).MatchString(msg.Header.Get("Auto-Submitted"))
		autoRespSupMatch := regexp.MustCompile(`(?i)(OOF|AutoReply|All)`).MatchString(msg.Header.Get("X-Auto-Response-Suppress"))

		hasListHeaders := msg.Header.Get("List-Id") != "" || msg.Header.Get("Mailing-List") != "" ||
			msg.Header.Get("X-Mailing-List") != "" || msg.Header.Get("X-list") != "" ||
			msg.Header.Get("List-Unsubscribe") != "" || msg.Header.Get("X-List-Post") != "" ||
			msg.Header.Get("X-List-Unsubscribe") != "" || msg.Header.Get("List-Post") != ""

		if senderMatch || precMatch || autoSubMatch || autoRespSupMatch || hasListHeaders {
			reply = false
			mylog("Skipping reply to [%s], assuming it is a mailinglist msg", sender)
		}
	}

	if reply {
		reNoReply := regexp.MustCompile(`(?i)(do[-_\.]?not?[-_\.]?reply|do[-_\.]?not?[-_\.]?respond|no[-_\.]?reply|newsletter|newsreview)`)
		if reNoReply.MatchString(sender) || reNoReply.MatchString(msg.Header.Get("From")) {
			reply = false
			mylog("Skipping reply to [%s], it has do not reply email address", sender)
		}
	}

	if reply { // ok, we really want to auto autorespond then.
		body := getAutoReplyText(user, msg, responseText)
		subject := getAutoReplySubject(user, msg, newSubject)
		fromAddress := getFromAddress(user, domain)

		// Construct email
		var buf bytes.Buffer
		buf.WriteString(fmt.Sprintf("To: %s\r\n", sender))
		buf.WriteString(fmt.Sprintf("From: %s\r\n", fromAddress))
		buf.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
		buf.WriteString("Auto-Submitted: auto-replied\r\n")
		buf.WriteString("X-Auto-Response-Suppress: All\r\n")
		buf.WriteString("\r\n") // End of headers
		buf.WriteString(body)

		// Send email
		err := sendSMTP(sender, fromAddress, buf.Bytes())
		if err != nil {
			mylog("Error sending mail to %s: %v", sender, err)
		} else {
			mylog("Sent autoresponse to %s.", sender)
			if DEBUG {
				mylog("body was : [%s]", body)
			}
			saveLastRepliedToValkey(vc, myemailaddress, sender, minElapsed)
		}
	}
}

// getConfigFromDB fetches per-account autoreply configuration from MySQL.
// Only the mail_accounts table (read-only config) is queried; no writes are made.
func getConfigFromDB(db *sql.DB, myemailaddress string) (int64, string, string) {
	var minTime sql.NullInt64
	var subject, text sql.NullString

	sql1 := "SELECT `autoreply_mintime`, `autoreply_subject`, `autoreply_text` FROM `mail_accounts` WHERE `account` LIKE ?"
	if DEBUG {
		mylog("SQL: %s", sql1)
	}
	err := db.QueryRow(sql1, myemailaddress).Scan(&minTime, &subject, &text)
	if err != nil {
		if DEBUG {
			if err == sql.ErrNoRows {
				mylog("No row found in mail_accounts for %s", myemailaddress)
			} else {
				mylog("Query error: %v", err)
			}
		}
	}

	if DEBUG {
		mylog("getConfigFromDB: minElapsed=%d subject=%s", minTime.Int64, subject.String)
	}

	return minTime.Int64, subject.String, text.String
}

// getLastRepliedFromValkey returns the Unix timestamp of the last autoreply
// sent from myemailaddress to sender, or 0 if no record exists.
// Key: autoreply:{myemailaddress}:{sender}  Value: Unix timestamp string
func getLastRepliedFromValkey(vc valkey.Client, myemailaddress, sender string) int64 {
	ctx := context.Background()
	key := valkeyKey(myemailaddress, sender)

	res, err := vc.Do(ctx, vc.B().Get().Key(key).Build()).ToString()
	if err != nil {
		// Miss (key expired or never set) is the normal case - not an error worth logging.
		if DEBUG && !valkey.IsValkeyNil(err) {
			mylog("Valkey GET error for key %s: %v", key, err)
		}
		return 0
	}

	t, err := strconv.ParseInt(res, 10, 64)
	if err != nil {
		mylog("Valkey: could not parse timestamp %q for key %s: %v", res, key, err)
		return 0
	}

	if DEBUG {
		mylog("Valkey: last replied timestamp for %s -> %s : %d", myemailaddress, sender, t)
	}
	return t
}

// saveLastRepliedToValkey records that we just sent an autoreply from myemailaddress
// to sender. The key expires after minElapsed seconds - exactly when we would be
// willing to reply again - so no manual cleanup is needed.
func saveLastRepliedToValkey(vc valkey.Client, myemailaddress, sender string, minElapsed int64) {
	ctx := context.Background()
	key := valkeyKey(myemailaddress, sender)
	val := strconv.FormatInt(time.Now().Unix(), 10)

	// If minElapsed is 0 or unset, default to 24 hours so the key doesn't persist forever.
	ttl := minElapsed
	if ttl <= 0 {
		ttl = 86400
	}

	err := vc.Do(ctx, vc.B().Set().Key(key).Value(val).Ex(time.Duration(ttl)*time.Second).Build()).Error()
	if err != nil {
		mylog("Valkey SET error for key %s: %v", key, err)
	} else if DEBUG {
		mylog("Valkey: saved reply timestamp for %s -> %s (TTL %ds)", myemailaddress, sender, ttl)
	}
}

func getAutoReplyText(user string, msg *mail.Message, text string) string {
	if DEBUG {
		mylog("getAutoReplyText: input:")
		mylog("user: %s", user)
		mylog("text: %s", text)
	}

	if text == "" {
		if DEBUG {
			mylog("Autoresponse text empty, using default")
		}
		text = DEFAULT_RESPONSE
	}

	subject := msg.Header.Get("Subject")
	dateStr := time.Now().Format(time.ANSIC)

	text = strings.ReplaceAll(text, "{S}", subject)
	text = strings.ReplaceAll(text, "{D}", dateStr)
	text = strings.ReplaceAll(text, "{USERNAME}", user)

	if DEBUG {
		mylog("getAutoReplyText: output:")
		mylog("text: %s", text)
	}
	return text + "\r\n"
}

func getAutoReplySubject(user string, msg *mail.Message, text string) string {
	if text == "" {
		if DEBUG {
			mylog("Autoresponse text empty, using default")
		}
		text = DEFAULT_SUBJECT
	}
	subject := msg.Header.Get("Subject")
	return strings.ReplaceAll(text, "{S}", subject)
}

func getFromAddress(user, domain string) string {
	defaultFrom := "Autoreply Daemon <AUTOREPLY_DAEMON@domain>"
	return strings.ReplaceAll(defaultFrom, "domain", domain)
}

func sendSMTP(to, from string, msgBytes []byte) error {
	addr := MAIL_SERVER
	if !strings.Contains(addr, ":") {
		addr = addr + ":25"
	}
	client, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := client.Mail(from); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}

	w, err := client.Data()
	if err != nil {
		return err
	}
	_, err = w.Write(msgBytes)
	if err != nil {
		return err
	}
	err = w.Close()
	if err != nil {
		return err
	}

	return client.Quit()
}
