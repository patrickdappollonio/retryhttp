package retryhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// nonReplayableReader is a simple reader that doesn't implement GetBody.
type nonReplayableReader struct {
	s    string
	read bool
}

func (nr *nonReplayableReader) Read(p []byte) (int, error) {
	if nr.read {
		return 0, io.EOF
	}
	nr.read = true
	return copy(p, nr.s), io.EOF
}

// Define a custom transport that implements CloseIdleConnections.
type testCloserTransport struct {
	rt     http.RoundTripper
	closed bool
}

// Implement the RoundTrip method.
func (tct *testCloserTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return tct.rt.RoundTrip(req)
}

// Implement the CloseIdleConnections method.
func (tct *testCloserTransport) CloseIdleConnections() {
	tct.closed = true
}

func TestClient_Do(t *testing.T) {
	t.Run("Successful Request", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		client := New(
			WithClient(ts.Client()),
			WithMaxRetries(2),
			WithInitialBackoff(10*time.Millisecond),
			WithBackoffMultiplier(2),
			WithMaxBackoff(50*time.Millisecond),
		)

		req, err := http.NewRequest("GET", ts.URL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		req = req.WithContext(ctx)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got: %d", resp.StatusCode)
		}
	})

	t.Run("Retry on 403 - eventual success", func(t *testing.T) {
		var attempts int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count := atomic.AddInt32(&attempts, 1)
			if count < 3 {
				w.WriteHeader(http.StatusForbidden)
			} else {
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer ts.Close()

		client := New(
			WithClient(ts.Client()),
			WithMaxRetries(5),
			WithInitialBackoff(10*time.Millisecond),
			WithBackoffMultiplier(2),
			WithMaxBackoff(50*time.Millisecond),
		)

		req, err := http.NewRequest("GET", ts.URL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		req = req.WithContext(ctx)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got: %d", resp.StatusCode)
		}
		if attempts != 3 {
			t.Fatalf("expected 3 attempts, got: %d", attempts)
		}
	})

	t.Run("No Retry on 403 with retry condition only on 400", func(t *testing.T) {
		var attempts int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attempts, 1)
			w.WriteHeader(http.StatusForbidden)
		}))
		defer ts.Close()

		retryOn400 := func(resp *http.Response, err error) bool {
			if err != nil {
				return true
			}
			if resp != nil && resp.StatusCode == http.StatusBadRequest {
				return true
			}
			return false
		}

		client := New(
			WithClient(ts.Client()),
			WithMaxRetries(5),
			WithCondition(retryOn400),
			WithInitialBackoff(10*time.Millisecond),
			WithBackoffMultiplier(2),
			WithMaxBackoff(50*time.Millisecond),
		)

		req, err := http.NewRequest("GET", ts.URL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		req = req.WithContext(ctx)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected status 403, got: %d", resp.StatusCode)
		}
		if attempts != 1 {
			t.Fatalf("expected 1 attempt, got: %d", attempts)
		}
	})

	t.Run("Context Cancellation due to slow server", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		client := New(
			WithClient(ts.Client()),
			WithMaxRetries(5),
			WithInitialBackoff(10*time.Millisecond),
			WithBackoffMultiplier(2),
			WithMaxBackoff(50*time.Millisecond),
		)

		req, err := http.NewRequest("GET", ts.URL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		req = req.WithContext(ctx)

		_, err = client.Do(req)
		if err == nil {
			t.Fatal("expected an error due to context timeout, got nil")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected context deadline exceeded error, got: %v", err)
		}
	})

	t.Run("Server Down - exhausting retries", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		ts.Close() // simulate down server

		client := New(
			WithClient(http.DefaultClient),
			WithMaxRetries(3),
			WithInitialBackoff(10*time.Millisecond),
			WithBackoffMultiplier(2),
			WithMaxBackoff(50*time.Millisecond),
		)

		req, err := http.NewRequest("GET", ts.URL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		req = req.WithContext(ctx)

		resp, err := client.Do(req)
		if err == nil {
			t.Fatal("expected error due to server down, got nil")
		}
		if resp != nil {
			t.Fatal("expected no response, but got one")
		}
	})

	t.Run("Request Body Replay", func(t *testing.T) {
		expectedBody := "test-body"
		var attempts int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("failed to read request body: %v", err)
			}
			if string(bodyBytes) != expectedBody {
				t.Errorf("expected body %q, got %q", expectedBody, string(bodyBytes))
			}
			count := atomic.AddInt32(&attempts, 1)
			if count == 1 {
				w.WriteHeader(http.StatusForbidden)
			} else {
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer ts.Close()

		client := New(
			WithClient(ts.Client()),
			WithMaxRetries(3),
			WithInitialBackoff(10*time.Millisecond),
			WithBackoffMultiplier(2),
			WithMaxBackoff(50*time.Millisecond),
		)

		req, err := http.NewRequest("POST", ts.URL, strings.NewReader(expectedBody))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		req = req.WithContext(ctx)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got: %d", resp.StatusCode)
		}
		if attempts != 2 {
			t.Fatalf("expected 2 attempts, got: %d", attempts)
		}
	})

	t.Run("Buffer Request Body With Nil GetBody", func(t *testing.T) {
		expectedBody := "buffer-test"
		var attempts int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("failed to read body: %v", err)
				return
			}
			if string(bodyBytes) != expectedBody {
				t.Errorf("expected body %q, got %q", expectedBody, string(bodyBytes))
			}
			count := atomic.AddInt32(&attempts, 1)
			if count == 1 {
				w.WriteHeader(http.StatusForbidden)
			} else {
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer ts.Close()

		client := New(
			WithClient(ts.Client()),
			WithMaxRetries(3),
			WithInitialBackoff(10*time.Millisecond),
			WithBackoffMultiplier(2),
			WithMaxBackoff(50*time.Millisecond),
		)

		req, err := http.NewRequest("POST", ts.URL, strings.NewReader(expectedBody))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		req = req.WithContext(ctx)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got: %d", resp.StatusCode)
		}
		if attempts != 2 {
			t.Fatalf("expected 2 attempts, got: %d", attempts)
		}
	})

	t.Run("Already Cancelled Context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		req, err := http.NewRequest("GET", "http://example.com", nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req = req.WithContext(ctx)

		client := New(
			WithClient(http.DefaultClient),
			WithMaxRetries(3),
			WithInitialBackoff(10*time.Millisecond),
			WithBackoffMultiplier(2),
			WithMaxBackoff(50*time.Millisecond),
		)

		resp, err := client.Do(req)
		if err == nil {
			t.Fatal("expected an error due to cancelled context, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled error, got: %v", err)
		}
		if resp != nil {
			t.Fatalf("expected no response, but got one")
		}
	})

	t.Run("Buffer Request Body With Nil GetBody - Custom Non-Replayable Reader", func(t *testing.T) {
		expectedBody := "buffer-test-non-replayable"
		var attempts int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("failed to read body: %v", err)
				return
			}
			if string(bodyBytes) != expectedBody {
				t.Errorf("expected body %q, got %q", expectedBody, string(bodyBytes))
			}
			count := atomic.AddInt32(&attempts, 1)
			if count == 1 {
				w.WriteHeader(http.StatusForbidden)
			} else {
				w.WriteHeader(http.StatusOK)
			}
		}))
		defer ts.Close()

		bodyReader := &nonReplayableReader{s: expectedBody}
		req, err := http.NewRequest("POST", ts.URL, bodyReader)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		if req.GetBody != nil {
			t.Fatalf("expected req.GetBody to be nil, but it was set")
		}

		client := New(
			WithClient(ts.Client()),
			WithMaxRetries(3),
			WithInitialBackoff(10*time.Millisecond),
			WithBackoffMultiplier(2),
			WithMaxBackoff(50*time.Millisecond),
		)
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		req = req.WithContext(ctx)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got: %d", resp.StatusCode)
		}
		if attempts != 2 {
			t.Fatalf("expected 2 attempts, got: %d", attempts)
		}
	})

	t.Run("MaxRetriesExceeded", func(t *testing.T) {
		var attempts int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&attempts, 1)
			// Always return a retryable status (403)
			w.WriteHeader(http.StatusForbidden)
		}))
		defer ts.Close()

		client := New(
			WithClient(ts.Client()),
			WithMaxRetries(2), // Total of 3 attempts (initial + 2 retries)
			WithInitialBackoff(10*time.Millisecond),
			WithBackoffMultiplier(2),
			WithMaxBackoff(50*time.Millisecond),
		)

		req, err := http.NewRequest("GET", ts.URL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		req = req.WithContext(ctx)

		resp, err := client.Do(req)
		if err == nil {
			t.Fatal("expected an error due to max retries exceeded, got nil")
		}
		if !errors.Is(err, ErrMaxRetriesExceeded) {
			t.Fatalf("expected error ErrMaxRetriesExceeded, got: %v", err)
		}
		if attempts != 3 {
			t.Fatalf("expected 3 attempts, got: %d", attempts)
		}
		// Optionally, verify the last response's status.
		if resp != nil && resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected last response status 403, got: %d", resp.StatusCode)
		}
	})
}

func TestClient_VerbMethods(t *testing.T) {
	t.Run("Get", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("expected GET, got %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("GET response"))
		}))
		defer ts.Close()

		client := New(WithClient(ts.Client()))
		resp, err := client.Get(ts.URL)
		if err != nil {
			t.Fatalf("Get returned error: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}
		resp.Body.Close()
	})

	t.Run("Head", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodHead {
				t.Errorf("expected HEAD, got %s", r.Method)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		client := New(WithClient(ts.Client()))
		resp, err := client.Head(ts.URL)
		if err != nil {
			t.Fatalf("Head returned error: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}
		resp.Body.Close()
	})

	t.Run("Post", func(t *testing.T) {
		expectedBody := "post body"
		expectedContentType := "application/json"
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			if ct := r.Header.Get("Content-Type"); ct != expectedContentType {
				t.Errorf("expected content-type %q, got %q", expectedContentType, ct)
			}
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("error reading body: %v", err)
			}
			if string(bodyBytes) != expectedBody {
				t.Errorf("expected body %q, got %q", expectedBody, string(bodyBytes))
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		client := New(WithClient(ts.Client()))
		resp, err := client.Post(ts.URL, expectedContentType, strings.NewReader(expectedBody))
		if err != nil {
			t.Fatalf("Post returned error: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}
		resp.Body.Close()
	})

	t.Run("PostForm", func(t *testing.T) {
		data := url.Values{}
		data.Set("key", "value")
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
				t.Errorf("expected content-type application/x-www-form-urlencoded, got %q", ct)
			}
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("error reading body: %v", err)
			}
			if string(bodyBytes) != data.Encode() {
				t.Errorf("expected body %q, got %q", data.Encode(), string(bodyBytes))
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		client := New(WithClient(ts.Client()))
		resp, err := client.PostForm(ts.URL, data)
		if err != nil {
			t.Fatalf("PostForm returned error: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected status 200, got %d", resp.StatusCode)
		}
		resp.Body.Close()
	})
}

func TestClient_CloseIdleConnections(t *testing.T) {
	// Create an instance of our custom transport.
	tct := &testCloserTransport{
		rt: http.DefaultTransport,
	}
	// Create an http.Client with the custom transport.
	httpClient := &http.Client{
		Transport: tct,
	}
	client := New(WithClient(httpClient))
	client.CloseIdleConnections()
	if !tct.closed {
		t.Fatal("expected CloseIdleConnections to be called on the transport")
	}
}
