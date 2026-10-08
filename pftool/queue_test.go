package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fixture = `{"queue_name": "deferred", "queue_id": "AAA111", "arrival_time": 1700000000, "message_size": 1200, "forced_expire": false, "sender": "alice@spam.example", "recipients": [{"address": "bob@remote.example", "delay_reason": "connect to remote.example[1.2.3.4]:25: Connection refused"}]}
{"queue_name": "hold", "queue_id": "BBB222", "arrival_time": 1700000001, "message_size": 1300, "forced_expire": false, "sender": "alice@spam.example", "recipients": [{"address": "carol@remote.example"}]}
{"queue_name": "active", "queue_id": "CCC333", "arrival_time": 1700000002, "message_size": 1400, "forced_expire": false, "sender": "dave@other.example", "recipients": [{"address": "Alice@Spam.Example"}, {"address": "erin@remote.example"}]}
{"queue_name": "hold", "queue_id": "DDD444", "arrival_time": 1700000003, "message_size": 1500, "forced_expire": false, "sender": "xalice@spam.example", "recipients": [{"address": "bob@remote.example"}]}
{"queue_name": "hold", "queue_id": "DDD444", "arrival_time": 1700000003, "message_size": 1500, "forced_expire": false, "sender": "xalice@spam.example", "recipients": [{"address": "bob@remote.example"}]}
{"queue_name": "hold", "queue_id": "EEE555", "arrival_time": 1700000004, "message_size": 1600, "forced_expire": false, "sender": "frank@sub.spam.example", "recipients": [{"address": "bob@remote.example"}]}
`

func loadFixture(t *testing.T) []queueEntry {
	t.Helper()
	entries, err := parseQueue(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestParseQueue(t *testing.T) {
	entries := loadFixture(t)
	if len(entries) != 6 {
		t.Fatalf("got %d entries", len(entries))
	}
	e := entries[0]
	if e.QueueName != "deferred" || e.QueueID != "AAA111" || e.Sender != "alice@spam.example" ||
		e.MessageSize != 1200 || len(e.Recipients) != 1 || e.Recipients[0].Address != "bob@remote.example" ||
		!strings.HasPrefix(e.Recipients[0].DelayReason, "connect to") {
		t.Errorf("first entry parsed as %+v", e)
	}
	if !entries[1].held() || entries[0].held() {
		t.Errorf("held flag wrong: %+v %+v", entries[0], entries[1])
	}
	if entries[2].Recipients[1].DelayReason != "" {
		t.Errorf("missing delay_reason must be empty")
	}
}

func TestParseQueueErrors(t *testing.T) {
	cases := map[string]string{
		"not json":         "postqueue: fatal: Queue report unavailable - mail system is down\n",
		"missing queue_id": `{"queue_name": "hold", "sender": "a@b"}` + "\n",
		"truncated":        `{"queue_name": "hold", "queue_id": "X"`,
		"sendmail format":  "-Queue ID-  --Size-- ----Arrival Time---- -Sender/Recipient-------\nAAA111!     1200 Tue Oct  7 00:00:00  alice@spam.example\n",
	}
	for name, in := range cases {
		if _, err := parseQueue(strings.NewReader(in)); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
	if entries, err := parseQueue(strings.NewReader("\n\n")); err != nil || len(entries) != 0 {
		t.Errorf("blank input: %v %v", entries, err)
	}
}

func TestMatchesAddress(t *testing.T) {
	cases := []struct {
		addr, key string
		want      bool
	}{
		{"alice@spam.example", "alice@spam.example", true},
		{"Alice@Spam.Example", "alice@spam.example", true},
		{"alice@spam.example", "spam.example", true},
		{"alice@spam.example", "SPAM.EXAMPLE", true},
		{"alice@spam.example", "@spam.", true},
		{"xk3j2q@spam.example", "spam", true},
		{"xalice@spam.example", "alice@spam.example", true},
		{"alice@spam.example", "lice@spam", true},
		{"alice@spam.example.community", "spam.example", true},
		{"frank@sub.spam.example", "spam.example", true},
		{"alice@spam.example", "bob", false},
		{"alice@spam.example", "spam.exampled", false},
		{"alice@spam.example", "", false},
		{"alice@spam.example", "   ", false},
		{"", "spam.example", false},
	}
	for _, c := range cases {
		if got := matchesAddress(c.addr, c.key); got != c.want {
			t.Errorf("matchesAddress(%q, %q) = %v, want %v", c.addr, c.key, got, c.want)
		}
	}
}

func TestSelections(t *testing.T) {
	entries := loadFixture(t)
	if got := selectHold(entries, "alice@spam.example"); !reflect.DeepEqual(got, []string{"AAA111", "CCC333"}) {
		t.Errorf("selectHold alice = %v (recipient match on CCC333, held BBB222 and DDD444 excluded)", got)
	}
	if got := selectRelease(entries, "alice@spam.example", -1); !reflect.DeepEqual(got, []string{"BBB222", "DDD444"}) {
		t.Errorf("selectRelease alice = %v (xalice is a substring match, duplicate listing collapses)", got)
	}
	if got := selectRelease(entries, "spam.example", -1); !reflect.DeepEqual(got, []string{"BBB222", "DDD444", "EEE555"}) {
		t.Errorf("selectRelease fragment = %v", got)
	}
	if got := selectRelease(entries, "spam.example", 1); !reflect.DeepEqual(got, []string{"BBB222"}) {
		t.Errorf("selectRelease max 1 = %v", got)
	}
	if got := selectRelease(entries, "spam.example", 0); len(got) != 0 {
		t.Errorf("selectRelease max 0 = %v", got)
	}
	if got := selectDelete(entries, "spam.example"); !reflect.DeepEqual(got, []string{"AAA111", "BBB222", "CCC333", "DDD444", "EEE555"}) {
		t.Errorf("selectDelete fragment = %v", got)
	}
	if got := selectDelete(entries, "other"); !reflect.DeepEqual(got, []string{"CCC333"}) {
		t.Errorf("selectDelete partial domain = %v", got)
	}
	if got := selectDelete(entries, "nobody@nowhere.example"); len(got) != 0 {
		t.Errorf("selectDelete no match = %v", got)
	}
}

// stubBinDir writes fake postqueue/postsuper/postcat scripts and points
// PFTOOL_BIN_DIR at them. postsuper records its arguments and stdin.
func stubBinDir(t *testing.T, postqueueOut string, postsuperExit int) (dir string) {
	t.Helper()
	dir = t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	qf := filepath.Join(dir, "postqueue.out")
	if err := os.WriteFile(qf, []byte(postqueueOut), 0o644); err != nil {
		t.Fatal(err)
	}
	write("postqueue", "#!/bin/sh\n[ \"$1\" = -j ] || exit 9\ncat "+qf+"\n")
	write("postsuper", "#!/bin/sh\necho \"$@\" > "+filepath.Join(dir, "postsuper.args")+"\ncat > "+filepath.Join(dir, "postsuper.stdin")+"\n"+
		"[ "+itoa(postsuperExit)+" -eq 0 ] || { echo 'postsuper: fatal: boom' >&2; exit "+itoa(postsuperExit)+"; }\n"+
		"echo 'postsuper: Placed on hold: 2 messages' >&2\n")
	write("postcat", "#!/bin/sh\necho \"postcat $@\"\n")
	t.Setenv("PFTOOL_BIN_DIR", dir)
	return dir
}

func itoa(n int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+n))) }

func TestListQueueViaStub(t *testing.T) {
	stubBinDir(t, fixture, 0)
	entries, err := listQueue()
	if err != nil || len(entries) != 6 {
		t.Fatalf("listQueue: %v, %d entries", err, len(entries))
	}
}

func TestListQueueFailure(t *testing.T) {
	dir := stubBinDir(t, "", 0)
	if err := os.WriteFile(filepath.Join(dir, "postqueue"), []byte("#!/bin/sh\necho 'postqueue: fatal: mail system is down' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := listQueue()
	if err == nil || !strings.Contains(err.Error(), "mail system is down") {
		t.Fatalf("want failure with stderr text, got %v", err)
	}
}

func TestPostsuperStdin(t *testing.T) {
	dir := stubBinDir(t, fixture, 0)
	if err := postsuper("-h", []string{"AAA111", "CCC333"}); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "postsuper.args"))
	stdin, _ := os.ReadFile(filepath.Join(dir, "postsuper.stdin"))
	if strings.TrimSpace(string(args)) != "-h -" {
		t.Errorf("postsuper args = %q", args)
	}
	if string(stdin) != "AAA111\nCCC333\n" {
		t.Errorf("postsuper stdin = %q", stdin)
	}
}

func TestPostsuperEmptyDoesNotRun(t *testing.T) {
	dir := stubBinDir(t, fixture, 0)
	if err := postsuper("-d", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "postsuper.args")); err == nil {
		t.Fatal("postsuper ran with no queue ids")
	}
}

func TestPostsuperFailure(t *testing.T) {
	stubBinDir(t, fixture, 1)
	err := postsuper("-d", []string{"AAA111"})
	if err == nil || !strings.Contains(err.Error(), "postsuper -d") {
		t.Fatalf("want postsuper failure, got %v", err)
	}
}
