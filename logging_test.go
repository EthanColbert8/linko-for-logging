package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func Test_loggerMiddleware(t *testing.T) {
	logBuffer := &bytes.Buffer{}

	logger := slog.New(slog.NewTextHandler(logBuffer, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Time(slog.TimeKey, time.Date(2023, 10, 1, 12, 34, 57, 0, time.UTC))
			}
			return a
		},
	}))

	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	loggedHandler := loggerMiddleware(logger, dummyHandler)

	req := httptest.NewRequest("GET", "http://lin.ko/api/stats", nil)
	rr := httptest.NewRecorder()
	loggedHandler.ServeHTTP(rr, req)

	const expectedLogString = `time=2023-10-01T12:34:57.000Z level=INFO msg="Served request" method=GET path=/api/stats client_ip=192.0.2.1:1234` + "\n"
	const expectedStatusCode = http.StatusOK

	receivedString := logBuffer.String()
	if receivedString != expectedLogString {
		t.Errorf("expected log output: %q, got: %q", expectedLogString, receivedString)
	}

	if rr.Code != expectedStatusCode {
		t.Errorf("expected status code: %d, got: %d", rr.Code, expectedStatusCode)
	}
}
