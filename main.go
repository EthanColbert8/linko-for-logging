package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
)

func main() {
	accessLog, err := os.Create("linko.access.log")
	if err != nil {
		log.Fatalf("failed to create access log file: %v", err)
	}
	defer accessLog.Close()

	standardLogger := log.New(os.Stderr, "DEBUG: ", log.LstdFlags)
	accessLogger := log.New(accessLog, "INFO: ", log.LstdFlags)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	status := run(ctx, cancel, *httpPort, *dataDir, standardLogger, accessLogger)
	cancel()
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string, standardLogger, accessLogger *log.Logger) int {
	st, err := store.New(dataDir, standardLogger)
	if err != nil {
		standardLogger.Printf("failed to create store: %v", err)
		return 1
	}
	s := newServer(*st, httpPort, cancel, accessLogger)
	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	standardLogger.Println("Linko is shutting down")

	if err := s.shutdown(shutdownCtx); err != nil {
		standardLogger.Printf("failed to shutdown server: %v", err)
		return 1
	}
	if serverErr != nil {
		standardLogger.Printf("server error: %v", serverErr)
		return 1
	}
	return 0
}
