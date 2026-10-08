package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	_ "embed"

	"github.com/alecthomas/kingpin/v2"
	"github.com/patrickdk77/postfix_exporter/config"
	"github.com/patrickdk77/postfix_exporter/exporter"
	"github.com/patrickdk77/postfix_exporter/logsource"
	"github.com/patrickdk77/postfix_exporter/showq"
	"github.com/prometheus/client_golang/prometheus"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	"github.com/prometheus/exporter-toolkit/web/kingpinflag"
)

var (
	//go:embed VERSION
	fallbackVersion string

	app                 *kingpin.Application
	toolkitFlags        *web.FlagConfig
	metricsPath         string
	postfixShowqPath    string
	postfixShowqPort    int
	postfixShowqNetwork string
	logUnsupportedLines bool
	instanceSpecs       []string
	postconfPath        string
	configFile          string
	queueDirectory      string

	instances []exporter.Instance
	cfg       *config.Config

	logConfig *promslog.Config

	cleanupLabels    []string
	lmtpLabels       []string
	pipeLabels       []string
	qmgrLabels       []string
	smtpLabels       []string
	smtpdLabels      []string
	bounceLabels     []string
	virtualLabels    []string
	postscreenLabels []string

	useWatchdog bool
)

func getShowqAddress(path, remoteAddr, network string, port int) string {
	switch network {
	case "unix":
		return path
	case "tcp", "tcp4", "tcp6":
		return fmt.Sprintf("%s:%d", remoteAddr, port)
	default:
		logFatal("Unsupported network type", "network", network)
		return ""
	}
}

func logFatal(msg string, args ...any) {
	slog.Error(msg, args...)
	os.Exit(1)
}

func initializeExporters(logSrcs []logsource.LogSourceCloser) []*exporter.PostfixExporter {
	exporters := make([]*exporter.PostfixExporter, 0, len(logSrcs)+1)

	if len(logSrcs) == 0 {
		slog.Info("No log source configured, collecting showq metrics only")
		s := showq.NewShowq(getShowqAddress(postfixShowqPath, "localhost", postfixShowqNetwork, postfixShowqPort)).
			WithNetwork(postfixShowqNetwork).
			WithQueueDirectory(queueDirectory)
		e := exporter.NewPostfixExporter(s, nil, logUnsupportedLines)
		prometheus.MustRegister(e)
		return append(exporters, e)
	}

	for _, logSrc := range logSrcs {
		showqAddr := getShowqAddress(postfixShowqPath, logSrc.RemoteAddr(), postfixShowqNetwork, postfixShowqPort)
		s := showq.NewShowq(showqAddr).WithNetwork(postfixShowqNetwork).WithConstLabels(logSrc.ConstLabels()).WithQueueDirectory(queueDirectory)
		exporter := exporter.NewPostfixExporter(
			s,
			logSrc,
			logUnsupportedLines,
			exporter.WithInstances(instances),
			exporter.WithConfig(cfg),
			exporter.WithCleanupLabels(cleanupLabels),
			exporter.WithLmtpLabels(lmtpLabels),
			exporter.WithPipeLabels(pipeLabels),
			exporter.WithQmgrLabels(qmgrLabels),
			exporter.WithSmtpLabels(smtpLabels),
			exporter.WithSmtpdLabels(smtpdLabels),
			exporter.WithBounceLabels(bounceLabels),
			exporter.WithVirtualLabels(virtualLabels),
			exporter.WithPostscreenLabels(postscreenLabels),
		)
		prometheus.MustRegister(exporter)
		exporters = append(exporters, exporter)
	}
	return exporters
}

func runExporter(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	logSrcs, err := logsource.NewLogSourceFromFactories(ctx)
	if err != nil {
		logFatal("Error opening log source", "error", err.Error())
	}
	exporters := initializeExporters(logSrcs)

	for i, e := range exporters {
		var src logsource.LogSourceCloser
		if i < len(logSrcs) {
			src = logSrcs[i]
		}
		go func() {
			err := e.StartMetricCollection(ctx)
			// rsyslog closes the pipe when it stops or restarts the
			// program, so standard input ending is a normal shutdown.
			if _, stdin := src.(*logsource.StdinLogSource); stdin && errors.Is(err, io.EOF) {
				slog.Info("Standard input closed, exiting")
				os.Exit(0)
			}
		}()
	}

	go func() {
		defer close(done)
		<-ctx.Done()
		for _, ls := range logSrcs {
			err := ls.Close()
			if err != nil {
				slog.Error("Error closing log source", "error", err.Error())
			}
		}
		for _, exporter := range exporters {
			prometheus.Unregister(exporter)
		}
	}()
	return done
}

func setupMetricsServer(versionString string) error {
	http.Handle(metricsPath, promhttp.Handler())
	lc := web.LandingConfig{
		Name:        "Postfix Exporter",
		Description: "Prometheus exporter for postfix metrics",
		Version:     versionString,
		Links: []web.LandingLinks{
			{
				Address: metricsPath,
				Text:    "Metrics",
			},
		},
	}
	lp, err := web.NewLandingPage(lc)
	if err != nil {
		return err
	}
	http.Handle("/", lp)
	return nil
}

func init() {
	if version.Version == "" {
		version.Version = strings.TrimSpace(fallbackVersion)
	}

	logConfig = &promslog.Config{}

	app = kingpin.New("postfix_exporter", "Prometheus metrics exporter for postfix")
	app.Version(version.Print("postfix_exporter"))
	app.Flag("watchdog", "Use watchdog to monitor log sources.").Default("false").BoolVar(&useWatchdog)
	toolkitFlags = kingpinflag.AddFlags(app, ":9154")
	app.Flag("web.telemetry-path", "Path under which to expose metrics.").Default("/metrics").StringVar(&metricsPath)
	app.Flag("postfix.showq_path", "Path at which Postfix places its showq socket.").Default("/var/spool/postfix/public/showq").StringVar(&postfixShowqPath)
	app.Flag("postfix.showq_port", "TCP port at which Postfix's showq service is listening.").Default("10025").IntVar(&postfixShowqPort)
	app.Flag("postfix.showq_network", "Network type for Postfix's showq service").Default("unix").StringVar(&postfixShowqNetwork)
	app.Flag("log.unsupported", "Log all unsupported lines.").BoolVar(&logUnsupportedLines)
	app.Flag("postfix.instance", "Syslog name whose log lines are parsed. Repeatable. Use service=name for a master.cf -o syslog_name override, or auto to read syslog_name and every override from postconf.").Default("postfix").StringsVar(&instanceSpecs)
	app.Flag("postfix.postconf_path", "postconf binary used by --postfix.instance=auto.").Default("postconf").StringVar(&postconfPath)
	app.Flag("postfix.queue_directory", "Postfix queue_directory, used to count the corrupt queue. Empty disables that count.").Default("/var/spool/postfix").StringVar(&queueDirectory)
	app.Flag("config.file", "Optional YAML file of rules that set the text label on reply metrics.").Default("").StringVar(&configFile)
	flag.AddFlags(app, logConfig)

	app.Flag("postfix.cleanup_service_label", "User-defined service labels for the cleanup service.").Default("cleanup").StringsVar(&cleanupLabels)
	app.Flag("postfix.lmtp_service_label", "User-defined service labels for the lmtp service.").Default("lmtp").StringsVar(&lmtpLabels)
	app.Flag("postfix.pipe_service_label", "User-defined service labels for the pipe service.").Default("pipe").StringsVar(&pipeLabels)
	app.Flag("postfix.qmgr_service_label", "User-defined service labels for the qmgr service.").Default("qmgr").StringsVar(&qmgrLabels)
	app.Flag("postfix.smtp_service_label", "User-defined service labels for the smtp service.").Default("smtp").StringsVar(&smtpLabels)
	app.Flag("postfix.smtpd_service_label", "User-defined service labels for the smtpd service.").Default("smtpd").StringsVar(&smtpdLabels)
	app.Flag("postfix.bounce_service_label", "User-defined service labels for the bounce service.").Default("bounce").StringsVar(&bounceLabels)
	app.Flag("postfix.virtual_service_label", "User-defined service labels for the virtual service.").Default("virtual").StringsVar(&virtualLabels)
	app.Flag("postfix.postscreen_service_label", "User-defined service labels for the postscreen service.").Default("postscreen").StringsVar(&postscreenLabels)

	app.HelpFlag.Short('h')

	logsource.InitLogSourceFactories(app)
}

func main() {
	kingpin.MustParse(app.Parse(os.Args[1:]))
	ctx := context.Background()
	logger := promslog.New(logConfig)
	slog.SetDefault(logger)

	logger.Info("Starting postfix_exporter", "version", version.Info())
	logger.Info("Build context", "build_context", version.BuildContext())

	var err error
	if instances, err = resolveInstances(instanceSpecs, postconfPath); err != nil {
		logFatal("Failed to resolve Postfix instances", "error", err.Error())
	}
	for _, instance := range instances {
		logger.Info("Parsing log lines", "syslog_name", instance.SyslogName, "service", instance.Service)
	}
	if configFile != "" {
		if cfg, err = config.Load(configFile); err != nil {
			logFatal("Failed to load config file", "error", err.Error())
		}
	}

	if err := setupMetricsServer(version.Info()); err != nil {
		logFatal("Failed to create landing page", "error", err.Error())
	}

	ctx, cancelFunc := context.WithCancel(ctx)
	defer cancelFunc()
	done := runExporter(ctx)

	// Start watchdog if enabled
	if useWatchdog {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			watchdogCtx := context.Background()
			defer ticker.Stop()
			for range ticker.C {
				if logsource.IsWatchdogUnhealthy(watchdogCtx) {
					slog.Warn("Watchdog: log source unhealthy, reloading")
					cancelFunc()
					if done != nil {
						<-done
					}
					ctx, cancelFunc = context.WithCancel(context.Background())
					done = runExporter(ctx)
				}
			}
		}()
	}

	prometheus.MustRegister(versioncollector.NewCollector("postfix_exporter"))

	server := &http.Server{}
	if err := web.ListenAndServe(server, toolkitFlags, logger); err != nil {
		logFatal("Error starting HTTP server", "error", err.Error())
	}
}
