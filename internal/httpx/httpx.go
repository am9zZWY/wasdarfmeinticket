// Package httpx is a small JSON-over-HTTP client with retries, because the
// upstream planners fail intermittently.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
)

// DefaultUserAgent is the browser identity sent upstream unless overridden.
const DefaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0"

// UserAgentEnv names the environment variable that overrides the identity.
const UserAgentEnv = "WASDARFMEINTICKET_USER_AGENT"

// IPv4OnlyEnv names the environment variable that, when set to a non-empty
// value, makes all upstream connections use IPv4. Useful where a host's
// IPv6 path to an upstream behaves differently from its IPv4 path.
const IPv4OnlyEnv = "WASDARFMEINTICKET_IPV4_ONLY"

// Client performs JSON requests with a bounded number of retries.
type Client struct {
	HTTP      *http.Client
	UserAgent string
	Retries   int           // additional attempts after the first one
	Backoff   time.Duration // wait before the first retry, doubled each time
}

// New returns a client with sensible defaults.
func New() *Client {
	userAgent := os.Getenv(UserAgentEnv)
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	if os.Getenv(IPv4OnlyEnv) != "" {
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", addr)
		}
		httpClient.Transport = transport
	}
	return &Client{
		HTTP:      httpClient,
		UserAgent: userAgent,
		Retries:   3,
		Backoff:   500 * time.Millisecond,
	}
}

// StatusError is returned for non-2xx responses.
type StatusError struct {
	URL    string
	Status int
}

func (statusError *StatusError) Error() string {
	// Without the query string: it is long and adds nothing for the reader.
	address, _, _ := strings.Cut(statusError.URL, "?")
	return fmt.Sprintf("%s: unexpected HTTP status %d", address, statusError.Status)
}

// IsStatus reports whether err is a StatusError with one of the given codes.
func IsStatus(err error, codes ...int) bool {
	var statusError *StatusError
	return errors.As(err, &statusError) && slices.Contains(codes, statusError.Status)
}

// GetJSON fetches url and decodes the JSON response into out.
func (client *Client) GetJSON(ctx context.Context, url string, out any) error {
	var lastErr error
	wait := client.Backoff
	for attempt := 0; attempt <= client.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			wait *= 2
		}
		retry, err := client.once(ctx, url, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry {
			break
		}
	}
	return lastErr
}

// once performs a single attempt and reports whether a retry makes sense.
func (client *Client) once(ctx context.Context, url string, out any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", client.UserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "de")
	resp, err := client.HTTP.Do(req)
	if err != nil {
		return ctx.Err() == nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return true, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// A rate limit (429) is not retried: asking again only makes it worse.
		return resp.StatusCode >= 500, &StatusError{URL: url, Status: resp.StatusCode}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, fmt.Errorf("%s: invalid JSON response: %w", url, err)
	}
	return false, nil
}
