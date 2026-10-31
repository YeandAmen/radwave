package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	token string
	http  *http.Client
}
type Metadata struct {
	Version int    `json:"currentVersionNumber"`
	Updated string `json:"lastUpdated"`
}

func NewClient(token string) *Client {
	client := &http.Client{Timeout: 180 * time.Second}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		host := req.URL.Hostname()
		if len(via) > 5 || req.URL.Scheme != "https" || (host != "www.kaggle.com" && host != "storage.googleapis.com" && !strings.HasSuffix(host, ".googleusercontent.com")) {
			return errors.New("Kaggle redirected to an unsupported download host")
		}
		if host != "www.kaggle.com" {
			req.Header.Del("Authorization")
		}
		return nil
	}
	return &Client{token: strings.TrimSpace(token), http: client}
}
func (c *Client) Configured() bool { return c != nil && strings.HasPrefix(c.token, "KGAT_") }
func (c *Client) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	if c.Configured() {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	r, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Kaggle request failed")
	}
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		r.Body.Close()
		return nil, fmt.Errorf("Kaggle returned HTTP %d; check dataset/API access", r.StatusCode)
	}
	return r, nil
}
func (c *Client) Metadata(ctx context.Context) (Metadata, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := c.get(ctx, "https://www.kaggle.com/api/v1/datasets/view/chandranshbinjola/job-listings")
	if err != nil {
		return Metadata{}, err
	}
	defer r.Body.Close()
	var m Metadata
	err = json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&m)
	if err != nil || m.Version < 1 {
		return m, errors.New("Kaggle dataset metadata was unreadable")
	}
	return m, nil
}
func (c *Client) Download(ctx context.Context, version int) ([]byte, error) {
	if !c.Configured() {
		return nil, errors.New("set KAGGLE_API_TOKEN for scheduled dataset downloads")
	}
	r, err := c.get(ctx, fmt.Sprintf("https://www.kaggle.com/api/v1/datasets/download/chandranshbinjola/job-listings?datasetVersionNumber=%d", version))
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, (128<<20)+1))
	if err != nil || len(data) > 128<<20 {
		return nil, errors.New("Kaggle download was incomplete or exceeded 128 MB")
	}
	return data, nil
}
