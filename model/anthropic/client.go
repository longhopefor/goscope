package anthropic

import (
	"bytes"
	"context"
	"fmt"
	"github.com/longhopefor/goscope/model"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	APIKey    string
	Model     string
	MaxTokens int
	// BaseURL 包含 /v1；默认为 https://api.anthropic.com/v1。
	BaseURL    string
	HTTPClient *http.Client
}
type Client struct {
	key, name, endpoint string
	http                *http.Client
	formatter           Formatter
}

var _ model.Model = (*Client)(nil)
var _ model.Streamer = (*Client)(nil)

func New(c Config) (*Client, error) {
	if strings.TrimSpace(c.APIKey) == "" || strings.TrimSpace(c.Model) == "" || c.MaxTokens < 0 {
		return nil, fmt.Errorf("APIKey and Model are required")
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://api.anthropic.com/v1"
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid BaseURL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return nil, fmt.Errorf("BaseURL requires HTTPS except loopback")
	}
	h := http.Client{Timeout: 60 * time.Second}
	if c.HTTPClient != nil {
		h = *c.HTTPClient
	}
	// 不把凭据或请求体转发到重定向地址。
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{key: c.APIKey, name: c.Model, endpoint: strings.TrimRight(c.BaseURL, "/") + "/messages", http: &h, formatter: Formatter{MaxTokens: c.MaxTokens}}, nil
}

// HTTPError 保留状态和请求 ID，不回显可能含密钥或用户输入的错误响应体。
type HTTPError struct {
	StatusCode int
	RequestID  string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("model HTTP status %d (request %s)", e.StatusCode, e.RequestID)
}
func (c *Client) send(ctx context.Context, r model.Request, stream bool) (*http.Response, error) {
	if c == nil || ctx == nil {
		return nil, fmt.Errorf("client and context are required")
	}
	raw, err := c.formatter.Encode(r, c.name, stream)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, &HTTPError{StatusCode: res.StatusCode, RequestID: res.Header.Get("request-id")}
	}
	return res, nil
}

const maxBody = 8 << 20

func (c *Client) Generate(ctx context.Context, r model.Request) (*model.Response, error) {
	res, err := c.send(ctx, r, false)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxBody {
		return nil, fmt.Errorf("model response exceeds size limit")
	}
	return c.formatter.Decode(raw)
}
