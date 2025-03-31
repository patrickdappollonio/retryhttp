package retryhttp

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type retryableclient interface {
	Do(req *http.Request) (*http.Response, error)
	CloseIdleConnections()
	Get(url string) (resp *http.Response, err error)
	Head(url string) (resp *http.Response, err error)
	Post(url, contentType string, body io.Reader) (resp *http.Response, err error)
	PostForm(url string, data url.Values) (resp *http.Response, err error)
}

var (
	_ retryableclient = (*Client)(nil)
	_ retryableclient = (*http.Client)(nil)
)

// ErrMaxRetriesExceeded is returned when the maximum number of retries is exceeded.
var ErrMaxRetriesExceeded = errors.New("max retries exceeded")

// RetryConditionFunc defines when a request should be retried.
type RetryConditionFunc func(resp *http.Response, err error) bool

// Client is our custom HTTP client with retry support.
type Client struct {
	client            *http.Client
	maxRetries        int
	retryCondition    RetryConditionFunc
	initialBackoff    time.Duration
	backoffMultiplier float64
	maxBackoff        time.Duration
}

// Option defines a function type to configure Client.
type Option func(*Client)

// WithClient sets the underlying HTTP client.
func WithClient(c *http.Client) Option {
	return func(cli *Client) {
		cli.client = c
	}
}

// WithMaxRetries sets the maximum number of retry attempts.
func WithMaxRetries(retries int) Option {
	return func(cli *Client) {
		cli.maxRetries = retries
	}
}

// WithCondition sets the retry condition function.
func WithCondition(cond RetryConditionFunc) Option {
	return func(cli *Client) {
		cli.retryCondition = cond
	}
}

// WithInitialBackoff sets the initial backoff duration.
func WithInitialBackoff(d time.Duration) Option {
	return func(cli *Client) {
		cli.initialBackoff = d
	}
}

// WithBackoffMultiplier sets the backoff multiplier.
func WithBackoffMultiplier(m float64) Option {
	return func(cli *Client) {
		cli.backoffMultiplier = m
	}
}

// WithMaxBackoff sets the maximum backoff duration.
func WithMaxBackoff(d time.Duration) Option {
	return func(cli *Client) {
		cli.maxBackoff = d
	}
}

// DefaultRetryCondition is used if no condition is provided.
// It retries on network errors and 4xx status codes.
func DefaultRetryCondition(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	if resp != nil {
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return true
		}
	}
	return false
}

// New creates a new Client using the provided options.
func New(opts ...Option) *Client {
	cli := &Client{
		client:            http.DefaultClient,
		maxRetries:        5,
		retryCondition:    DefaultRetryCondition,
		initialBackoff:    100 * time.Millisecond,
		backoffMultiplier: 2,
		maxBackoff:        2 * time.Second,
	}
	for _, opt := range opts {
		opt(cli)
	}
	return cli
}

// Do sends an HTTP request with retry logic. It is a drop-in replacement for http.Client.Do.
// It buffers the request body (if any) so that it can be replayed on retries, while leaving response
// bodies untouched for streaming. The response body is only closed if a retry is needed.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	var resp *http.Response
	var err error

	// Buffer the request body if necessary.
	if req.Body != nil && req.GetBody == nil {
		bodyBytes, readErr := io.ReadAll(req.Body)
		if readErr != nil {
			return nil, readErr
		}
		req.Body.Close()
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
		// Reset the request body for the first attempt.
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	backoff := c.initialBackoff

	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		// Check for context cancellation.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// For retries, reset the request body if available.
		if attempt > 0 && req.Body != nil && req.GetBody != nil {
			newBody, getErr := req.GetBody()
			if getErr != nil {
				return nil, getErr
			}
			req.Body = newBody
		}

		resp, err = c.client.Do(req)

		// Check for cancellation after the request.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Return immediately if retry is not required.
		if !c.retryCondition(resp, err) {
			return resp, err
		}

		// Close the response body if retryable.
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}

		// Wait for the backoff period or until context cancellation.
		select {
		case <-time.After(backoff):
			backoff = time.Duration(float64(backoff) * c.backoffMultiplier)
			if backoff > c.maxBackoff {
				backoff = c.maxBackoff
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if err == nil {
		err = ErrMaxRetriesExceeded
	}
	return resp, err
}

// transport returns the underlying RoundTripper used by the client.
// If c.client.Transport is nil, it returns http.DefaultTransport.
func (c *Client) transport() http.RoundTripper {
	if c.client.Transport != nil {
		return c.client.Transport
	}
	return http.DefaultTransport
}

// CloseIdleConnections closes any connections on the underlying Transport
// which are sitting idle in a "keep-alive" state. If the Transport does not
// implement CloseIdleConnections, this method does nothing.
func (c *Client) CloseIdleConnections() {
	type closeIdler interface {
		CloseIdleConnections()
	}
	if tr, ok := c.transport().(closeIdler); ok {
		tr.CloseIdleConnections()
	}
}

// Get issues a GET request to the specified URL. It is a drop-in replacement for http.Client.Get.
func (c *Client) Get(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// Head issues a HEAD request to the specified URL. It is a drop-in replacement for http.Client.Head.
func (c *Client) Head(url string) (*http.Response, error) {
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// Post issues a POST request to the specified URL with the given content type and body.
// It is a drop-in replacement for http.Client.Post.
func (c *Client) Post(url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return c.Do(req)
}

// PostForm issues a POST request with form data (URL-encoded) to the specified URL.
// It is a drop-in replacement for http.Client.PostForm.
func (c *Client) PostForm(urlStr string, data url.Values) (*http.Response, error) {
	return c.Post(urlStr, "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
}
