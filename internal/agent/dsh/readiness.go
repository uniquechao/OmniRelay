package dsh

import (
	"context"
	"net/http"
	"time"
)

// Readiness uses the same peer identity and endpoint as work dispatch.
func (c *Client) Readiness(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/readyz", nil)
	if err != nil {
		return "unreachable"
	}
	response, err := c.http.Do(req)
	if err != nil {
		return "unreachable"
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		return "ready"
	case http.StatusServiceUnavailable:
		return "not_ready"
	default:
		return "unreachable"
	}
}
