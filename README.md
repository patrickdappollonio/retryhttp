# retryhttp

`retryhttp` is a Go library that provides a custom HTTP client with robust retry logic, including exponential backoff and context cancellation support. It's designed to be a drop-in enhancement over the standard `http.Client`, allowing you to easily retry HTTP requests based on customizable conditions.

It includes features like:

- **Customizable Retry Logic:**
  Retry HTTP requests based on user-defined conditions (e.g. specific HTTP status codes like 403 or 4xx errors, network errors, etc).

- **Bring-your-own HTTP Client:**
  Use your own `http.Client` instance, allowing for custom transport settings, timeouts, and more.

- **Exponential Backoff:**
  Automatically increase the delay between retries using exponential backoff.

- **Context Cancellation:**
  Fully supports Go's `context` package to handle request timeouts and cancellations gracefully. We read the request's context and check it before we even try to make the request and after we get a response.
  This ensures that if the context is done, the process short-circuits.

- **Functional Options Configuration:**
  Configure your client using idiomatic functional options such as `WithClient()`, `WithCondition()`, `WithMaxRetries()`, etc.

- **Request Body Replay:**
  Buffers the request body when necessary to allow retries without disrupting streaming of the response body.

## Installation

To install `retryhttp`, run:

```sh
go get github.com/patrickdappollonio/retryhttp@latest
```

## Usage

Here's a basic example to get you started:

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/patrickdappollonio/retryhttp"
)

func main() {
	// Create a new client with custom settings.
	client := retryhttp.New(
		retryhttp.WithClient(http.DefaultClient),
		retryhttp.WithMaxRetries(5),
		retryhttp.WithInitialBackoff(100*time.Millisecond),
		retryhttp.WithBackoffMultiplier(2),
		retryhttp.WithMaxBackoff(2*time.Second),
		retryhttp.WithCondition(func(resp *http.Response, err error) bool {
			if err != nil {
				return true // retry on non-nil error
			}
			if resp != nil {
				// Retry on HTTP 429 Too Many Requests status code.
				// You can customize this condition based on your needs.
				return resp.StatusCode == http.StatusTooManyRequests
			}
			return false
		}),
	)

	// Create a new HTTP request.
	req, err := http.NewRequest("GET", "http://example.com", nil)
	if err != nil {
		fmt.Printf("Error creating request: %v\n", err)
		return
	}

	// Set up a context with timeout and attach it to the request.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	// Send the request using the retry-enabled client.
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Request failed: %v\n", err)
		return
	}
	defer resp.Body.Close()

	fmt.Printf("Request succeeded with status: %s\n", resp.Status)
}
```

## API

### Creating a Client

Use the `New` function along with functional options to configure your client:

```go
client := retryhttp.New(
    retryhttp.WithClient(http.DefaultClient),
    retryhttp.WithMaxRetries(5),
    retryhttp.WithInitialBackoff(100*time.Millisecond),
    retryhttp.WithBackoffMultiplier(2),
    retryhttp.WithMaxBackoff(2*time.Second),
    retryhttp.WithCondition(func(resp *http.Response, err error) bool {
        // Define your retry condition here.
        return false
    }),
)
```

### Options

- **`WithClient(c *http.Client)` Option**
  Set a custom underlying HTTP client.

- **`WithMaxRetries(retries int)` Option**
  Specify the maximum number of retry attempts.

- **`WithCondition(cond RetryConditionFunc)` Option**
  Set a custom function to determine whether a retry should occur.

- **`WithInitialBackoff(d time.Duration)` Option**
  Specify the initial backoff duration.

- **`WithBackoffMultiplier(m float64)` Option**
  Specify the multiplier for the exponential backoff.

- **`WithMaxBackoff(d time.Duration)` Option**
  Specify the maximum backoff duration.

### Executing Requests

Call the `Do` method on your client to execute a request with retry logic:

```go
resp, err := client.Do(req)
```

- **Request Body Replay:**
  If the request has a body and `GetBody` is nil, `Do` buffers the body so that it can be replayed on retries. This ensures that response bodies remain untouched (except when closing after a failed attempt).

- **Context Integration:**
  The request honors the provided context for cancellation and timeouts.

### Other `http.Client` Methods

The remaining methods of the standard library's `http.Client` are also available on the `retryhttp.Client` struct, allowing you to use it as a drop-in replacement:

```go
CloseIdleConnections()
Get(url string) (resp *http.Response, err error)
Head(url string) (resp *http.Response, err error)
Post(url, contentType string, body io.Reader) (resp *http.Response, err error)
PostForm(url string, data url.Values) (resp *http.Response, err error)
```

They've been copied as much as possible from the standard library, but they are not guaranteed to be identical.

## Contributing

Contributions are welcome! Please open issues or submit pull requests for improvements, bug fixes, or additional features.
