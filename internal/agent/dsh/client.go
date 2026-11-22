package dsh

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

type Binding struct {
	Kind       string `json:"kind,omitempty"`
	Work       string `json:"work"`
	Agent      string `json:"agent"`
	Owner      string `json:"owner"`
	Epoch      int64  `json:"epoch"`
	Generation int64  `json:"generation"`
	Session    string `json:"session"`
}
type Result struct {
	Session string `json:"session"`
	Turn    int    `json:"turn"`
}

// TLS pins the private CA and presents this service's own certificate in both directions.
func TLS(ca, cert, key string) (*tls.Config, error) {
	pem, err := os.ReadFile(ca)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("invalid agent CA")
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, RootCAs: roots, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert}, nil
}

type Client struct {
	base string
	http *http.Client
}

func NewClient(endpoint string, certs *tls.Config) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("invalid private Runtime URL")
	}
	return &Client{base: "https://" + u.Host, http: &http.Client{Transport: &http.Transport{TLSClientConfig: certs}, Timeout: 85 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Query the durable execution before submitting or resuming it on the same owner.
func (c *Client) Execute(ctx context.Context, b Binding) (Result, error) {
	endpoint := c.base + "/works/" + url.PathEscape(b.Work)
	query, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := c.http.Do(query)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode == 200 {
		return decode(resp)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 && resp.StatusCode != 202 {
		return Result{}, errors.New("runtime status unavailable")
	}
	payload, _ := json.Marshal(b)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = c.http.Do(req)
	if err != nil {
		return Result{}, err
	}
	return decode(resp)
}
func decode(resp *http.Response) (Result, error) {
	defer resp.Body.Close()
	var r Result
	if resp.StatusCode != 200 {
		return r, errors.New("runtime has no completed execution")
	}
	err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&r)
	if err == nil && (r.Session == "" || r.Turn < 0) {
		err = errors.New("invalid runtime completion")
	}
	return r, err
}
