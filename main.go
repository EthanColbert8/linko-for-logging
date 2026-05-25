package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"boot.dev/linko/internal/build"
	"boot.dev/linko/internal/linkoerr"
	"boot.dev/linko/internal/store"
	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	pkgerr "github.com/pkg/errors"
	"gopkg.in/natefinch/lumberjack.v2"
)

var sensitiveKeys = []string{
	"password",
	"key",
	"apiKey",
	"secret",
	"pin",
	"creditcardno",
	"dsn",
	"user",
}

type closeFunc func() error

type stackTracer interface {
	error
	StackTrace() pkgerr.StackTrace
}

type multiError interface {
	error
	Unwrap() []error
}

func main() {
	logger, logCloser, err := initializeLogger()
	if err != nil {
		fmt.Printf("failed to initialize logger: %v", err)
		os.Exit(1)
	}
	// defer logCloser() // logCloser is called right before explicit `os.Exit(status)`

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir, logger)
	cancel()

	logCloser()
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string, logger *slog.Logger) int {
	st, err := store.New(dataDir, logger)
	if err != nil {
		logger.Error("failed to create store", "error", err)
		return 1
	}
	s := newServer(*st, httpPort, cancel, logger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	logger.Debug("Linko is shutting down")

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shutdown server", "error", err)
		return 1
	}
	if serverErr != nil {
		logger.Error("server error", "error", serverErr)
		return 1
	}
	return 0
}

func initializeLogger() (*slog.Logger, closeFunc, error) {
	logFileName := os.Getenv("LINKO_LOG_FILE")

	var newLogger *slog.Logger = nil
	var rotatedLogger *lumberjack.Logger = nil

	noColor := !(isatty.IsCygwinTerminal(os.Stderr.Fd()) || isatty.IsTerminal(os.Stderr.Fd()))
	debugOptions := tint.Options{
		NoColor:     noColor,
		Level:       slog.LevelDebug,
		ReplaceAttr: replaceAttr,
	}
	stderrLogHandler := tint.NewHandler(os.Stderr, &debugOptions)

	if logFileName != "" {
		rotatedLogger = &lumberjack.Logger{
			Filename:   logFileName,
			MaxSize:    1,
			MaxAge:     28,
			MaxBackups: 10,
			LocalTime:  false,
			Compress:   true,
		}

		infoOptions := slog.HandlerOptions{
			Level:       slog.LevelInfo,
			ReplaceAttr: replaceAttr,
		}
		fileLogHandler := slog.NewJSONHandler(rotatedLogger, &infoOptions)

		theLogHandler := slog.NewMultiHandler(fileLogHandler, stderrLogHandler)
		newLogger = slog.New(theLogHandler)
	} else {
		newLogger = slog.New(stderrLogHandler)
	}

	hostName, _ := os.Hostname()

	// Add build and runtime info to all logs
	newLogger = newLogger.With(
		slog.String("git_sha", build.GitSHA),
		slog.String("build_time", build.BuildTime),
		slog.String("env", os.Getenv("ENV")),
		slog.String("hostname", hostName),
	)

	return newLogger, getCloseLogsFunc(rotatedLogger), nil
}

func getCloseLogsFunc(logger *lumberjack.Logger) closeFunc {
	return func() error {
		if logger == nil {
			return nil
		}

		err := logger.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to close logger: %v", err)
		}

		return err
	}
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "error" {
		err, ok := a.Value.Any().(error)
		if !ok {
			return a
		}

		if multiErr, ok := errors.AsType[multiError](err); ok {
			var errorsAttrs []slog.Attr

			for i, errn := range multiErr.Unwrap() {
				errnAttrs := errorAttrs(errn)
				errorsAttrs = append(errorsAttrs, slog.GroupAttrs(fmt.Sprintf("error_%d", i+1), errnAttrs...))
			}

			return slog.GroupAttrs("errors", errorsAttrs...)
		}

		attrs := errorAttrs(err)
		return slog.GroupAttrs("error", attrs...)
	}

	// last-resort security filters
	if slices.Contains(sensitiveKeys, a.Key) {
		return slog.String(a.Key, "[REDACTED]")
	} else if a.Value.Kind() == slog.KindString && strings.Contains(strings.ToLower(a.Key), "url") {
		if newUrlString, wasUrl := sanitizeURL(a.Value.String()); wasUrl {
			return slog.String(a.Key, newUrlString)
		}
	}

	return a
}

func errorAttrs(err error) []slog.Attr {
	attrs := linkoerr.Attrs(err)

	errMessage := slog.Attr{
		Key:   "message",
		Value: slog.StringValue(err.Error()),
	}
	attrs = append(attrs, errMessage)

	if stackErr, ok := errors.AsType[stackTracer](err); ok {
		errTrace := slog.Attr{
			Key:   "stack_trace",
			Value: slog.StringValue(fmt.Sprintf("%+v", stackErr.StackTrace())),
		}
		attrs = append(attrs, errTrace)
	}

	return attrs
}

func sanitizeURL(theUrl string) (string, bool) {
	urlValue, err := url.Parse(theUrl)
	if err != nil {
		return "", false
	}

	if _, passwordSet := urlValue.User.Password(); passwordSet {
		urlValue.User = url.UserPassword(urlValue.User.Username(), "REDACTED")
	}

	return urlValue.String(), true
}
