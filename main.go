package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/build"
	"boot.dev/linko/internal/linkoerr"
	"boot.dev/linko/internal/store"
	pkgerr "github.com/pkg/errors"
)

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
	var logFile *os.File = nil
	var bufferedFile *bufio.Writer = nil
	var err error

	debugOptions := slog.HandlerOptions{
		Level:       slog.LevelDebug,
		ReplaceAttr: replaceAttr,
	}
	stderrLogHandler := slog.NewTextHandler(os.Stderr, &debugOptions)

	if logFileName != "" {
		logFile, err = os.Create(logFileName)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to create log file: %w", err)
		}
		bufferedFile = bufio.NewWriterSize(logFile, 8192)

		infoOptions := slog.HandlerOptions{
			Level:       slog.LevelInfo,
			ReplaceAttr: replaceAttr,
		}
		fileLogHandler := slog.NewJSONHandler(bufferedFile, &infoOptions)

		theLogHandler := slog.NewMultiHandler(fileLogHandler, stderrLogHandler)
		newLogger = slog.New(theLogHandler)
	} else {
		newLogger = slog.New(stderrLogHandler)
	}

	newLogger = newLogger.With(
		slog.String("git_sha", build.GitSHA),
		slog.String("build_time", build.BuildTime),
	)

	return newLogger, getCloseLogsFunc(logFile, bufferedFile), nil
}

func getCloseLogsFunc(file *os.File, buffer *bufio.Writer) closeFunc {
	return func() error {
		err1 := buffer.Flush()
		if err1 != nil {
			fmt.Fprintf(os.Stderr, "failed to flush log buffer: %v", err1)
		}

		err2 := file.Close()
		if err2 != nil {
			fmt.Fprintf(os.Stderr, "error closing file: %v", err2)
		}

		return err1
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
