package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		got = request.Header.Get("User-Agent")
		writer.Write([]byte(`{}`))
	}))
	defer srv.Close()
	var out map[string]any

	if err := New().GetJSON(context.Background(), srv.URL, &out); err != nil || got != DefaultUserAgent {
		t.Errorf("default: %q, %v", got, err)
	}
	t.Setenv(UserAgentEnv, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Test")
	if err := New().GetJSON(context.Background(), srv.URL, &out); err != nil || got != "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Test" {
		t.Errorf("override: %q, %v", got, err)
	}
}

func TestIPv4Only(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Write([]byte(`{}`))
	}))
	defer srv.Close() // listens on 127.0.0.1
	_, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	var out map[string]any

	t.Setenv(IPv4OnlyEnv, "1")
	client := New()
	client.Retries = 0
	if err := client.GetJSON(context.Background(), srv.URL, &out); err != nil {
		t.Errorf("IPv4 address with IPv4 only: %v", err)
	}
	if err := client.GetJSON(context.Background(), "http://[::1]:"+port, &out); err == nil {
		t.Error("IPv6 address with IPv4 only: expected a dial error")
	}
}

func TestRateLimitIsNotRetried(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		writer.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	client := New()
	client.Retries, client.Backoff = 3, 1
	var out map[string]any
	err := client.GetJSON(context.Background(), srv.URL+"/x?secret=1&long=query", &out)
	if err == nil || calls != 1 {
		t.Errorf("err = %v, calls = %d", err, calls)
	}
	if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "/x: unexpected HTTP status 429") {
		t.Errorf("error text = %q", err)
	}
}
