package showq

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type Showq struct {
	mu                sync.Mutex // Protects histogram/gauge operations during concurrent scrapes
	ageHistogram      *prometheus.HistogramVec
	sizeHistogram     *prometheus.HistogramVec
	queueMessageGauge *prometheus.GaugeVec
	recipientGauge    *prometheus.GaugeVec
	delayedGauge      *prometheus.GaugeVec
	forcedGauge       *prometheus.GaugeVec
	corruptGauge      prometheus.Gauge
	knownQueues       map[string]struct{}
	constLabels       prometheus.Labels
	address           string
	network           string
	queueDirectory    string
	once              sync.Once
}

// reasonReply finds the remote reply code in a deferral reason such as
// "host mx.example.com[192.0.2.1] said: 450 4.7.1 Try again later".
var reasonReply = regexp.MustCompile(`said: (\d{3})[ -](?:(\d\.\d{1,3}\.\d{1,3}) )?`)

type delayedKey struct {
	queue, code, enhancedCode string
}

// ScanNullTerminatedEntries is a splitting function for bufio.Scanner
// to split entries by null bytes.
func ScanNullTerminatedEntries(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		// Valid record found.
		return i + 1, data[0:i], nil
	} else if atEOF && len(data) != 0 {
		// Data at the end of the file without a null terminator.
		return 0, nil, errors.New("expected null byte terminator")
	} else {
		// Request more data.
		return 0, nil, nil
	}
}

// CollectBinaryShowqFromReader parses Postfix's binary showq format.
func (s *Showq) collectBinaryShowqFromReader(file io.Reader, ch chan<- prometheus.Metric) error {
	err := s.collectBinaryShowqFromScanner(file)
	s.queueMessageGauge.Collect(ch)
	s.recipientGauge.Collect(ch)
	s.delayedGauge.Collect(ch)
	s.forcedGauge.Collect(ch)

	s.sizeHistogram.Collect(ch)
	s.ageHistogram.Collect(ch)
	return err
}

func (s *Showq) collectBinaryShowqFromScanner(file io.Reader) error {
	scanner := bufio.NewScanner(file)
	scanner.Split(ScanNullTerminatedEntries)
	queueSizes := make(map[string]float64)
	recipients := make(map[string]float64)
	forced := make(map[string]float64)
	delayed := make(map[delayedKey]float64)

	// HistogramVec is intended to capture data streams. Showq however always returns all emails
	// currently queued, therefore we need to reset the histograms before every collect.
	s.sizeHistogram.Reset()
	s.ageHistogram.Reset()

	now := float64(time.Now().UnixNano()) / 1e9
	queue := "unknown"
	for scanner.Scan() {
		// Parse a key/value entry.
		key := scanner.Text()
		if len(key) == 0 {
			// Empty key means a record separator.
			queue = "unknown"
			continue
		}
		if !scanner.Scan() {
			return fmt.Errorf("key %q does not have a value", key)
		}
		value := scanner.Text()

		switch key {
		case "queue_name":
			// The name of the message queue.
			queue = value
			queueSizes[queue]++
		case "size":
			// Message size in bytes.
			size, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return err
			}
			s.sizeHistogram.WithLabelValues(queue).Observe(size)
		case "time":
			// Message time as a UNIX timestamp.
			utime, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return err
			}
			s.ageHistogram.WithLabelValues(queue).Observe(now - utime)
		case "recipient":
			recipients[queue]++
		case "reason":
			// Set for recipients whose delivery was deferred.
			if value != "" {
				key := delayedKey{queue: queue}
				if m := reasonReply.FindStringSubmatch(value); m != nil {
					key.code, key.enhancedCode = m[1], m[2]
				}
				delayed[key]++
			}
		case "forced_expire":
			if value == "1" {
				forced[queue]++
			}
		}
	}

	s.recipientGauge.Reset()
	s.delayedGauge.Reset()
	s.forcedGauge.Reset()
	for q := range s.knownQueues {
		s.recipientGauge.WithLabelValues(q).Set(recipients[q])
		s.forcedGauge.WithLabelValues(q).Set(forced[q])
	}
	for q, count := range recipients {
		s.recipientGauge.WithLabelValues(q).Set(count)
	}
	for q, count := range forced {
		s.forcedGauge.WithLabelValues(q).Set(count)
	}
	for key, count := range delayed {
		s.delayedGauge.WithLabelValues(key.queue, key.code, key.enhancedCode).Set(count)
	}

	for q, count := range queueSizes {
		s.queueMessageGauge.WithLabelValues(q).Set(count)
	}
	for q := range s.knownQueues {
		if _, seen := queueSizes[q]; !seen {
			s.queueMessageGauge.WithLabelValues(q).Set(0)
			s.sizeHistogram.WithLabelValues(q)
			s.ageHistogram.WithLabelValues(q)
		}
	}
	return scanner.Err()
}

func (s *Showq) init() {
	s.once.Do(func() {
		s.ageHistogram = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace:   "postfix",
				Name:        "showq_message_age_seconds",
				Help:        "Age of messages in Postfix's message queue, in seconds",
				Buckets:     []float64{1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8},
				ConstLabels: s.constLabels,
			},
			[]string{"queue"})
		s.sizeHistogram = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace:   "postfix",
				Name:        "showq_message_size_bytes",
				Help:        "Size of messages in Postfix's message queue, in bytes",
				Buckets:     []float64{1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9},
				ConstLabels: s.constLabels,
			},
			[]string{"queue"})
		s.queueMessageGauge = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace:   "postfix",
				Name:        "showq_queue_depth",
				Help:        "Number of messages in Postfix's message queue",
				ConstLabels: s.constLabels,
			},
			[]string{"queue"},
		)
		s.recipientGauge = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace:   "postfix",
				Name:        "showq_queue_recipients",
				Help:        "Number of recipients in Postfix's message queue",
				ConstLabels: s.constLabels,
			},
			[]string{"queue"},
		)
		s.delayedGauge = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace:   "postfix",
				Name:        "showq_delayed_recipients",
				Help:        "Number of queued recipients with a deferral reason, by the remote reply code in that reason (empty when the reason has none, such as a connection timeout)",
				ConstLabels: s.constLabels,
			},
			[]string{"queue", "code", "enhanced_code"},
		)
		s.forcedGauge = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace:   "postfix",
				Name:        "showq_forced_expire_messages",
				Help:        "Number of messages in Postfix's message queue marked for forced expiration",
				ConstLabels: s.constLabels,
			},
			[]string{"queue"},
		)
		s.corruptGauge = prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace:   "postfix",
			Name:        "queue_corrupt_messages",
			Help:        "Number of files in Postfix's corrupt queue directory, which showq does not report",
			ConstLabels: s.constLabels,
		})
		s.knownQueues = map[string]struct{}{"active": {}, "deferred": {}, "hold": {}, "incoming": {}, "maildrop": {}}
	})
}

func (s *Showq) Collect(ch chan<- prometheus.Metric) error {
	// Lock BEFORE opening socket to serialize all showq queries
	// This prevents concurrent connections to showq daemon which may not handle them properly
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	s.collectCorrupt(ch)

	fd, err := net.Dial(s.network, s.address)
	if err != nil {
		return err
	}
	defer fd.Close()

	return s.collectBinaryShowqFromReader(fd, ch)
}

// collectCorrupt counts the files in the corrupt queue directory. A
// missing directory counts as empty; any other read error skips the
// metric.
func (s *Showq) collectCorrupt(ch chan<- prometheus.Metric) {
	if s.queueDirectory == "" {
		return
	}
	entries, err := os.ReadDir(filepath.Join(s.queueDirectory, "corrupt"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("Failed to read the corrupt queue", "error", err.Error())
		return
	}
	s.corruptGauge.Set(float64(len(entries)))
	ch <- s.corruptGauge
}

// WithQueueDirectory sets Postfix's queue_directory, used to count the
// corrupt queue. An empty directory disables that count.
func (s *Showq) WithQueueDirectory(dir string) *Showq {
	s.queueDirectory = dir
	return s
}

func (s *Showq) Path() string {
	return fmt.Sprintf("%s://%s", s.network, s.address)
}

func (s *Showq) WithConstLabels(labels prometheus.Labels) *Showq {
	s.constLabels = labels
	return s
}

func (s *Showq) WithNetwork(network string) *Showq {
	s.network = network
	return s
}

func NewShowq(addr string) *Showq {
	return &Showq{
		address: addr,
		network: "unix",
	}
}
