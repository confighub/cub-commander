// Package cubclient is a small REST client for the ConfigHub list API. The
// engine is entity-generic, so it wants rows as maps rather than the typed
// goclient structs; auth and server come from the CUB_* environment cub sets
// when it runs a plugin.
package cubclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Row = map[string]any

type Client struct {
	http   *http.Client
	base   string
	token  string
	spaces map[string]string // slug → id
}

func New() (*Client, error) {
	server := os.Getenv("CUB_SERVER")
	if server == "" {
		return nil, fmt.Errorf("CUB_SERVER not set; run this as a cub plugin (cub commander …)")
	}
	token := os.Getenv("CUB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("CUB_TOKEN not set; run 'cub auth login' first")
	}
	return &Client{
		http:   &http.Client{Transport: &retryTransport{next: http.DefaultTransport}, Timeout: 2 * time.Minute},
		base:   strings.TrimRight(server, "/") + "/api",
		token:  token,
		spaces: map[string]string{},
	}, nil
}

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("server %d: %s", e.Status, e.Message) }

// List performs a GET returning a JSON array of extended rows.
func (c *Client) List(ctx context.Context, path string, q url.Values) ([]Row, error) {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		var e struct{ Message string }
		_ = json.Unmarshal(body, &e)
		msg := strings.TrimSpace(e.Message)
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		return nil, &APIError{res.StatusCode, msg}
	}
	var rows []Row
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return rows, nil
}

// GetRaw performs a GET returning the body as text (unit data is YAML).
func (c *Client) GetRaw(ctx context.Context, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	if res.StatusCode >= 300 {
		return "", &APIError{res.StatusCode, strings.TrimSpace(string(body))}
	}
	return string(body), nil
}

// SpaceID resolves a space slug to its ID, caching per client.
func (c *Client) SpaceID(ctx context.Context, slug string) (string, error) {
	if id, ok := c.spaces[slug]; ok {
		return id, nil
	}
	q := url.Values{"where": {fmt.Sprintf("Slug = '%s'", slug)}, "select": {"Slug"}}
	rows, err := c.List(ctx, "/space", q)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("no space with slug %q", slug)
	}
	sp, _ := rows[0]["Space"].(map[string]any)
	id, _ := sp["SpaceID"].(string)
	if id == "" {
		return "", fmt.Errorf("space %q: no SpaceID in response", slug)
	}
	c.spaces[slug] = id
	return id, nil
}

type retryTransport struct{ next http.RoundTripper }

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	const attempts = 4
	var res *http.Response
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			delay := time.Duration(float64(time.Second) * float64(int(1)<<uint(i-1)) * (0.7 + 0.6*rand.Float64()))
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(delay):
			}
		}
		res, err = t.next.RoundTrip(req)
		if err == nil && res.StatusCode != 429 && res.StatusCode < 500 {
			return res, nil
		}
		if res != nil && i < attempts-1 {
			res.Body.Close()
		}
	}
	return res, err
}
