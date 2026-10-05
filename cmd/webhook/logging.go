package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"

	log "github.com/sirupsen/logrus"
	"sigs.k8s.io/external-dns/provider"
	webhookapi "sigs.k8s.io/external-dns/provider/webhook/api"
)

func webhookHandler(p provider.Provider, logRequests, logResponses bool) http.Handler {
	api := &webhookapi.WebhookServer{Provider: p}
	mux := http.NewServeMux()
	mux.HandleFunc("/", api.NegotiateHandler)
	mux.HandleFunc(webhookapi.UrlRecords, api.RecordsHandler)
	mux.HandleFunc(webhookapi.UrlAdjustEndpoints, api.AdjustEndpointsHandler)
	return logWebhookTraffic(mux, logRequests, logResponses)
}

func logWebhookTraffic(next http.Handler, logRequests, logResponses bool) http.Handler {
	if !logRequests && !logResponses {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Buffer responses only when enabled, so logging completes before any reply.
		var response *bufferedResponse
		target := w
		if logResponses {
			response = &bufferedResponse{header: make(http.Header)}
			target = response
		}
		if logRequests {
			dump, err := httputil.DumpRequest(r, true)
			if err != nil {
				log.WithError(err).Warn("failed to log webhook request")
				http.Error(target, "failed to read request body", http.StatusBadRequest)
			} else {
				log.WithField("request", string(dump)).Info("webhook request")
				next.ServeHTTP(target, r)
			}
		} else {
			next.ServeHTTP(target, r)
		}
		if response != nil {
			if response.status == 0 {
				response.WriteHeader(http.StatusOK)
			}
			log.WithFields(log.Fields{
				"method": r.Method, "url": r.URL.String(),
				"status": response.status, "headers": response.sentHeader,
				"body": response.body.String(),
			}).Info("webhook response")
			for key, values := range response.sentHeader {
				w.Header()[key] = values
			}
			w.WriteHeader(response.status)
			if _, err := io.Copy(w, &response.body); err != nil {
				log.WithError(err).Warn("failed to write webhook response")
			}
		}
	})
}

// bufferedResponse holds the webhook's finite JSON response until it is logged.
type bufferedResponse struct {
	header     http.Header
	sentHeader http.Header
	status     int
	body       bytes.Buffer
}

func (w *bufferedResponse) Header() http.Header { return w.header }
func (w *bufferedResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.sentHeader = w.header.Clone()
}
func (w *bufferedResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		if _, ok := w.header["Content-Type"]; !ok && w.header.Get("Content-Encoding") == "" {
			w.header.Set("Content-Type", http.DetectContentType(body))
		}
		w.WriteHeader(http.StatusOK)
	}
	if w.status == http.StatusNoContent || w.status == http.StatusNotModified || w.status < 200 {
		return 0, http.ErrBodyNotAllowed
	}
	return w.body.Write(body)
}
