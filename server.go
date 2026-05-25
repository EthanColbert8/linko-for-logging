package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"boot.dev/linko/internal/store"
)

type spyReadCloser struct {
	io.ReadCloser
	bytesRead int
}

func (r *spyReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytesRead += n
	return n, err
}

type spyResponseWriter struct {
	http.ResponseWriter
	bytesWritten int
	statusCode   int
}

func (w *spyResponseWriter) Write(p []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytesWritten += n
	return n, err
}

func (w *spyResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

const logContextKey contextKey = "log_context"

type LogContext struct {
	Username string
	Error    error
}

func httpError(ctx context.Context, w http.ResponseWriter, status int, err error) {
	if logCtx, ok := ctx.Value(logContextKey).(*LogContext); ok {
		logCtx.Error = err
	}

	// sanitize error text sent to user
	errorText := err.Error()
	if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusInternalServerError {
		errorText = http.StatusText(status)
	}

	http.Error(w, errorText, status)
}

type server struct {
	httpServer *http.Server
	store      store.Store
	cancel     context.CancelFunc
	logger     *slog.Logger
}

func newServer(store store.Store, port int, cancel context.CancelFunc, logger *slog.Logger) *server {
	mux := http.NewServeMux()

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: loggerMiddleware(logger, requestIdMiddleware(mux)),
	}

	s := &server{
		httpServer: srv,
		store:      store,
		cancel:     cancel,
		logger:     logger,
	}

	mux.HandleFunc("GET /", s.handlerIndex)
	mux.Handle("POST /api/login", s.authMiddleware(http.HandlerFunc(s.handlerLogin)))
	mux.Handle("POST /api/shorten", s.authMiddleware(http.HandlerFunc(s.handlerShortenLink)))
	mux.Handle("GET /api/stats", s.authMiddleware(http.HandlerFunc(s.handlerStats)))
	mux.Handle("GET /api/urls", s.authMiddleware(http.HandlerFunc(s.handlerListURLs)))
	mux.HandleFunc("GET /{shortCode}", s.handlerRedirect)
	mux.HandleFunc("POST /admin/shutdown", s.handlerShutdown)

	return s
}

func (s *server) start() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}

	s.logger.Debug(fmt.Sprintf("Linko is running on http://localhost:%d", ln.Addr().(*net.TCPAddr).Port))

	if err := s.httpServer.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *server) shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *server) handlerShutdown(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("ENV") == "production" {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
	go s.cancel()
}

func loggerMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqId := r.Header.Get("X-Request-ID")

		spyReader := spyReadCloser{ReadCloser: r.Body}
		r.Body = &spyReader

		logContextValue := &LogContext{Username: "", Error: nil}
		r = r.WithContext(context.WithValue(r.Context(), logContextKey, logContextValue))

		spyWriter := spyResponseWriter{ResponseWriter: w}

		start := time.Now()
		next.ServeHTTP(&spyWriter, r)
		reqDuration := time.Since(start)

		logAttrs := []any{
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.String("client_ip", r.RemoteAddr),
			slog.Duration("duration", reqDuration),
			slog.String("request_id", reqId),
			slog.Int("request_body_bytes", spyReader.bytesRead),
			slog.Int("response_status", spyWriter.statusCode),
			slog.Int("response_body_bytes", spyWriter.bytesWritten),
		}

		if logContextValue.Username != "" {
			logAttrs = append(logAttrs, slog.String("user", logContextValue.Username))
		}
		if logContextValue.Error != nil {
			logAttrs = append(logAttrs, slog.Any("error", logContextValue.Error))
		}

		logger.Info("Served request", logAttrs...)
	})
}

func requestIdMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqId := r.Header.Get("X-Request-ID")

		if reqId == "" {
			reqId = rand.Text()
		}

		w.Header().Set("X-Request-ID", reqId)
		next.ServeHTTP(w, r)
	})
}
