package main

import (
	"flag"
	"os"
	"os/signal"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/thehaven/docker-net-dhcp/pkg/plugin"
)

var (
	logLevel      = flag.String("log", "", "log level")
	logFile       = flag.String("logfile", "", "log file")
	logMaxSize    = flag.Int("log-max-size", 0, "maximum size in megabytes before rotating log file")
	logMaxBackups = flag.Int("log-max-backups", 0, "maximum number of old log files to retain")
	logMaxAge     = flag.Int("log-max-age", 0, "maximum number of days to retain old log files")
	logCompress   = flag.Bool("log-compress", true, "compress rotated log files with gzip")
	bindSock      = flag.String("sock", "/run/docker/plugins/net-dhcp.sock", "bind unix socket")
)

func main() {
	flag.Parse()

	if *logLevel == "" {
		if *logLevel = os.Getenv("LOG_LEVEL"); *logLevel == "" {
			*logLevel = "info"
		}
	}

	level, err := log.ParseLevel(*logLevel)
	if err != nil {
		log.WithError(err).Fatal("Failed to parse log level")
	}
	log.SetLevel(level)

	if *logFile == "" {
		*logFile = os.Getenv("LOG_FILE")
	}

	maxSize := 50
	if *logMaxSize > 0 {
		maxSize = *logMaxSize
	} else if v, ok := os.LookupEnv("LOG_MAX_SIZE"); ok {
		if i, err := strconv.Atoi(v); err == nil && i > 0 {
			maxSize = i
		}
	}

	maxBackups := 5
	if *logMaxBackups > 0 {
		maxBackups = *logMaxBackups
	} else if v, ok := os.LookupEnv("LOG_MAX_BACKUPS"); ok {
		if i, err := strconv.Atoi(v); err == nil && i >= 0 {
			maxBackups = i
		}
	}

	maxAge := 14
	if *logMaxAge > 0 {
		maxAge = *logMaxAge
	} else if v, ok := os.LookupEnv("LOG_MAX_AGE"); ok {
		if i, err := strconv.Atoi(v); err == nil && i >= 0 {
			maxAge = i
		}
	}

	compress := *logCompress
	if v, ok := os.LookupEnv("LOG_COMPRESS"); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			compress = b
		}
	}

	if *logFile != "" {
		rotator := &lumberjack.Logger{
			Filename:   *logFile,
			MaxSize:    maxSize,
			MaxBackups: maxBackups,
			MaxAge:     maxAge,
			Compress:   compress,
		}
		defer rotator.Close()
		log.StandardLogger().Out = rotator
	}

	awaitTimeout := 5 * time.Second
	if t, ok := os.LookupEnv("AWAIT_TIMEOUT"); ok {
		awaitTimeout, err = time.ParseDuration(t)
		if err != nil {
			log.WithError(err).Fatal("Failed to parse await timeout")
		}
	}

	p, err := plugin.NewPlugin(awaitTimeout)
	if err != nil {
		log.WithError(err).Fatal("Failed to create plugin")
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, unix.SIGINT, unix.SIGTERM)

	go func() {
		log.Info("Starting server...")
		if err := p.Listen(*bindSock); err != nil {
			log.WithError(err).Fatal("Failed to start plugin")
		}
	}()

	<-sigs
	log.Info("Shutting down...")
	if err := p.Close(); err != nil {
		log.WithError(err).Fatal("Failed to stop plugin")
	}
}
