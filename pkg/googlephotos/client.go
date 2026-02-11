package googlephotos

import (
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

const (
	maxRetries  = 5
	baseBackoff = 5 * time.Second
	minJitter   = 100
	jitterRange = 250
)

type Client struct {
	client *http.Client
	logger *slog.Logger
}

func NewClient(logger *slog.Logger) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		client: &http.Client{
			Jar: jar,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return nil
			},
			Timeout: 120 * time.Second,
		},
		logger: logger,
	}
}

func (c *Client) Get(targetURL string) (*http.Response, error) {
	return c.doWithRetry(func() (*http.Request, error) {
		req, err := http.NewRequest("GET", targetURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		return req, nil
	})
}

// Head performs a lightweight HEAD request without jitter (used for content-type probing)
func (c *Client) Head(targetURL string) (*http.Response, error) {
	req, err := http.NewRequest("HEAD", targetURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	return c.client.Do(req)
}

// Post performs a POST request with retry logic and cookie/session support
func (c *Client) Post(targetURL string, contentType string, body string) (*http.Response, error) {
	return c.doWithRetry(func() (*http.Request, error) {
		req, err := http.NewRequest("POST", targetURL, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Content-Type", contentType)
		return req, nil
	})
}

// doWithRetry executes a request with jitter, rate-limit retry and exponential backoff
func (c *Client) doWithRetry(makeReq func() (*http.Request, error)) (*http.Response, error) {
	jitter := time.Duration(minJitter+rand.Intn(jitterRange)) * time.Millisecond
	time.Sleep(jitter)

	var resp *http.Response
	for i := 0; i < maxRetries; i++ {
		req, err := makeReq()
		if err != nil {
			return nil, err
		}

		resp, err = c.client.Do(req)
		if err != nil {
			return nil, err
		}

		// Success or client error (4xx except 429) — return immediately
		if resp.StatusCode < 429 || (resp.StatusCode > 429 && resp.StatusCode < 500) {
			return resp, nil
		}

		// Retryable: 429 (rate limit) or 5xx (server error)
		resp.Body.Close()
		sleepTime := baseBackoff * time.Duration(i+1)
		if resp.StatusCode == 429 {
			if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
				if seconds, parseErr := time.ParseDuration(retryAfter + "s"); parseErr == nil {
					sleepTime = seconds
				}
			}
		}
		c.logger.Warn("Retryable HTTP error, retrying", "status", resp.StatusCode, "sleep", sleepTime, "attempt", i+1)
		time.Sleep(sleepTime)
	}

	return resp, nil
}
