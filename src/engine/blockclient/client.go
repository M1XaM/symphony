// Package blockclient talks to a block's admin port (health, control,
// metrics, logs) on behalf of the engine. One client per container.
package blockclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"symphony/blocks/common/control"
	"symphony/blocks/common/health"
	"symphony/blocks/common/metrics"
)

// Client reaches one block's admin API.
type Client struct {
	Base  string // http://host:port
	Token string
	http  *http.Client
}

var shared = &http.Client{Transport: &http.Transport{
	MaxIdleConns:        128,
	MaxIdleConnsPerHost: 4,
	IdleConnTimeout:     30 * time.Second,
	DialContext:         (&net.Dialer{Timeout: 500 * time.Millisecond}).DialContext,
}}

// New builds a client for host:port with the deployment's admin token.
func New(addr, token string) *Client {
	return &Client{Base: "http://" + addr, Token: token, http: shared}
}

// Addr is the host:port the client talks to.
func (c *Client) Addr() string { return strings.TrimPrefix(c.Base, "http://") }

// LogLine mirrors blocks/common/server.LogLine.
type LogLine struct {
	Ts    int64          `json:"ts"`
	Level string         `json:"level"`
	Msg   string         `json:"msg"`
	Attrs map[string]any `json:"attrs,omitempty"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any, timeout time.Duration) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, c.Base+path, rd)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("block rejected the admin token")
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode %s (%d): %w", path, resp.StatusCode, err)
		}
	}
	return nil
}

// Health fetches the deep health document (any status is a 200).
func (c *Client) Health(ctx context.Context) (health.Document, error) {
	var d health.Document
	err := c.do(ctx, http.MethodGet, "/health", nil, &d, 800*time.Millisecond)
	return d, err
}

// Control sends one command; a command the block refused is an error.
func (c *Client) Control(ctx context.Context, cmd control.Command) (control.Result, error) {
	var r control.Result
	if err := c.do(ctx, http.MethodPost, "/control", cmd, &r, 3*time.Second); err != nil {
		return r, err
	}
	if !r.OK {
		return r, fmt.Errorf("%s: %s", cmd.Op, r.Error)
	}
	return r, nil
}

// Metrics fetches the counters and histograms, with the histograms'
// percentiles taken over the last window (0: the block's default).
func (c *Client) Metrics(ctx context.Context, window time.Duration) (metrics.Document, error) {
	var d metrics.Document
	path := "/metrics"
	if window > 0 {
		path += fmt.Sprintf("?window_ms=%d", window.Milliseconds())
	}
	err := c.do(ctx, http.MethodGet, path, nil, &d, 800*time.Millisecond)
	return d, err
}

// Logs fetches the last n log lines.
func (c *Client) Logs(ctx context.Context, n int) ([]LogLine, error) {
	var out struct {
		Lines []LogLine `json:"lines"`
	}
	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/logs?n=%d", n), nil, &out, 2*time.Second)
	return out.Lines, err
}
