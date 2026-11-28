package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const (
	// maxAttempts is how many times one call is sent before its error is
	// returned.
	maxAttempts = 3

	// defaultRateLimitWait is used for a 429 that does not say how long to
	// wait. No probe could produce a 429, so its format is unverified.
	defaultRateLimitWait = 30 * time.Second
	maxRateLimitWait     = 60 * time.Second

	firstBackoff = 2 * time.Second
)

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// do sends one request for the calls in records.go and nameservers.go, and
// retries it when retrying can help. It is separate from execute, which the
// frozen godaddy_domain_record resource uses and which must not change.
//
// Every call made through do is idempotent, so sending it again cannot
// compound a write. It retries a 429 after the wait GoDaddy asks for, and a
// 5xx or a transport failure after a growing pause, up to maxAttempts in all.
//
// Bodies are not logged. A non-nil result receives the decoded response.
func (c *Client) do(ctx context.Context, customerID, method, path string, body, result interface{}) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}

	backoff := firstBackoff
	for attempt := 1; ; attempt++ {
		wait, err := c.attempt(ctx, customerID, method, path, payload, result)
		if err == nil {
			return nil
		}
		if wait < 0 || attempt == maxAttempts || ctx.Err() != nil {
			return err
		}
		if wait == 0 {
			wait = backoff
			backoff *= 2
		}
		tflog.Debug(ctx, "retrying GoDaddy request", map[string]interface{}{
			"method": method, "path": path, "attempt": attempt, "wait": wait.String(), "error": err.Error(),
		})
		if sleepErr := c.sleep(ctx, wait); sleepErr != nil {
			return err
		}
	}
}

// attempt sends the request once. On failure it also says how to retry: a
// negative wait means do not; zero means retry after the caller's backoff;
// a positive wait means retry after exactly that long.
func (c *Client) attempt(ctx context.Context, customerID, method, path string, payload []byte, result interface{}) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return -1, err
	}
	if customerID != "" {
		req.Header.Set(headerCustomerID, customerID)
	}
	req.Header.Set(headerAccept, mediaTypeJSON)
	if payload != nil {
		req.Header.Set(headerContent, mediaTypeJSON)
	}
	req.Header.Set(headerAuthorization, c.authorization)

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)

	tflog.Debug(ctx, "GoDaddy request", map[string]interface{}{
		"method": method, "path": path, "status": resp.StatusCode,
	})

	if resp.StatusCode >= http.StatusBadRequest {
		apiErr := newAPIError(method, path, resp.StatusCode, resp.Header, respBody)
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			wait := apiErr.RetryAfter
			if wait <= 0 {
				wait = defaultRateLimitWait
			}
			if wait > maxRateLimitWait {
				wait = maxRateLimitWait
			}
			return wait, apiErr
		case resp.StatusCode >= http.StatusInternalServerError:
			return 0, apiErr
		default:
			return -1, apiErr
		}
	}
	if readErr != nil {
		return 0, readErr
	}
	if result == nil || len(respBody) == 0 {
		return -1, nil
	}
	if err := json.Unmarshal(respBody, result); err != nil {
		return -1, err
	}
	return -1, nil
}
