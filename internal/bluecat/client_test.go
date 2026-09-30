package bluecat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoginAndListZones(t *testing.T) {
	token := testAccessToken(t, time.Now().Add(time.Hour))
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer refresh", r.Header.Get("Authorization"))
		require.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token})
	})
	mux.HandleFunc("/api/v2/zones", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
		require.Contains(t, r.URL.Query().Get("filter"), "example.com")
		_ = json.NewEncoder(w).Encode(collection[Zone]{
			Data: []Zone{{ID: ptr(int64(7)), AbsoluteName: ptr("example.com")}},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := Login(context.Background(), Config{Host: srv.URL, RefreshToken: "refresh", TokenExchangeURL: srv.URL + "/token"})
	require.NoError(t, err)

	zones, err := client.ListZones(context.Background(), "example.com")
	require.NoError(t, err)
	require.Len(t, zones, 1)
	require.Equal(t, "example.com", zones[0].AbsoluteNameOrEmpty())
}

func TestCreateHostUsesRelativeName(t *testing.T) {
	var got map[string]any
	token := testAccessToken(t, time.Now().Add(time.Hour))
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer refresh", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token})
	})
	mux.HandleFunc("/api/v2/zones/7/resourceRecords", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &got))
		w.WriteHeader(http.StatusCreated)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := Login(context.Background(), Config{Host: srv.URL, RefreshToken: "refresh", TokenExchangeURL: srv.URL + "/token"})
	require.NoError(t, err)

	err = client.CreateOrUpdateHost(context.Background(), Zone{ID: ptr(int64(7)), AbsoluteName: ptr("example.com")}, HostRecord{
		AbsoluteName: ptr("app.example.com"),
		Addresses:    []Address{{Address: ptr("192.0.2.10")}},
	})
	require.NoError(t, err)
	require.Equal(t, "HostRecord", got["type"])
	require.Equal(t, "app", got["name"])
	require.Nil(t, got["absoluteName"])
}

func TestRelativeName(t *testing.T) {
	require.Equal(t, "app", relativeName("app.example.com", "example.com"))
	require.Equal(t, "", relativeName("example.com", "example.com"))
}

func testAccessToken(t *testing.T, expires time.Time) string {
	t.Helper()
	payload, err := json.Marshal(map[string]int64{"exp": expires.Unix()})
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".test-signature"
}

func TestAccessTokenCacheAndRefresh(t *testing.T) {
	tokens := []string{
		testAccessToken(t, time.Now().Add(time.Hour)),
		testAccessToken(t, time.Now().Add(2*time.Hour)),
	}
	var exchanges atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "Bearer refresh", r.Header.Get("Authorization"))
		n := exchanges.Add(1)
		if n > int32(len(tokens)) {
			http.Error(w, "unexpected exchange", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": tokens[n-1]})
	})
	mux.HandleFunc("/api/v2/zones", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+tokens[exchanges.Load()-1], r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(collection[Zone]{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client, err := Login(context.Background(), Config{
		Host: srv.URL, RefreshToken: "refresh", TokenExchangeURL: srv.URL + "/token",
	})
	require.NoError(t, err)
	c := client.(*httpClient)
	require.Equal(t, tokens[0], c.token)
	for range 2 {
		_, err = client.ListZones(context.Background(), "")
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, exchanges.Load())

	// Expire the cache directly so the test does not depend on sleeping.
	c.tokenMu.Lock()
	c.tokenExpires = time.Now().Add(-time.Second)
	c.tokenMu.Unlock()
	for range 2 {
		_, err = client.ListZones(context.Background(), "")
		require.NoError(t, err)
	}
	require.EqualValues(t, 2, exchanges.Load())
	require.Equal(t, tokens[1], c.token)
}

func TestLoginRejectsInvalidTokenResponses(t *testing.T) {
	expired := testAccessToken(t, time.Now().Add(-time.Hour))
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"unauthorized", http.StatusUnauthorized, "denied", "http 401"},
		{"invalid JSON", http.StatusOK, "{", "decode token exchange response"},
		{"missing token", http.StatusOK, "{}", "missing access_token"},
		{"invalid JWT", http.StatusOK, `{"access_token":"invalid"}`, "not a JWT"},
		{"missing expiration", http.StatusOK, `{"access_token":"header.e30.signature"}`, "missing or expired exp"},
		{"expired", http.StatusOK, `{"access_token":"` + expired + `"}`, "missing or expired exp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(srv.Close)
			client, err := Login(context.Background(), Config{
				Host: srv.URL, RefreshToken: "refresh", TokenExchangeURL: srv.URL,
			})
			require.ErrorContains(t, err, tc.want)
			require.Nil(t, client)
		})
	}
}
