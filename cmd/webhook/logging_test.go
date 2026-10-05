package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
)

func TestLogWebhookTraffic(t *testing.T) {
	logger := log.StandardLogger()
	oldOutput, oldLevel := logger.Out, logger.GetLevel()
	defer func() { logger.SetOutput(oldOutput); logger.SetLevel(oldLevel) }()
	logger.SetLevel(log.InfoLevel)
	for _, requests := range []bool{false, true} {
		for _, responses := range []bool{false, true} {
			t.Run(fmt.Sprintf("requests=%t/responses=%t", requests, responses), func(t *testing.T) {
				var logs bytes.Buffer
				logger.SetOutput(&logs)
				out := httptest.NewRecorder()
				handler := logWebhookTraffic(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil || string(body) != "request payload" {
						t.Fatalf("request body: %q, %v", body, err)
					}
					if requests && !strings.Contains(logs.String(), "request payload") {
						t.Fatal("request not logged before handling")
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"result":"ok"}`))
					if responses && out.Body.Len() != 0 {
						t.Fatal("response sent before logging")
					}
				}), requests, responses)
				handler.ServeHTTP(out, httptest.NewRequest(http.MethodPost, "/records", strings.NewReader("request payload")))
				if out.Code != http.StatusCreated || out.Body.String() != `{"result":"ok"}` || out.Header().Get("Content-Type") != "application/json" {
					t.Fatalf("changed response: %+v", out)
				}
				if strings.Contains(logs.String(), "webhook request") != requests {
					t.Fatalf("request logs: %s", &logs)
				}
				if strings.Contains(logs.String(), "webhook response") != responses {
					t.Fatalf("response logs: %s", &logs)
				}
				if responses && (!strings.Contains(logs.String(), "status=201") || !strings.Contains(logs.String(), "result")) {
					t.Fatalf("missing response details: %s", &logs)
				}
			})
		}
	}
}

func TestBufferedResponseEmptyAndError(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent, http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			out := httptest.NewRecorder()
			logWebhookTraffic(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if status != http.StatusOK {
					w.WriteHeader(status)
				}
			}), false, true).ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/records", nil))
			if out.Code != status || out.Body.Len() != 0 {
				t.Fatalf("unexpected response: %+v", out)
			}
		})
	}
}
