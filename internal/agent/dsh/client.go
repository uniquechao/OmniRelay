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

	"github.com/uniquechao/OmniRelay/internal/routing"
)

var ErrConfiguration = errors.New("runtime rejected agent configuration")
var ErrWorkBindingConflict = errors.New("runtime work binding differs from its journal")

// A native turn can use four minutes. Leave time to close its host, return
// the HTTP result and commit the Gateway receipt while the claim is live.
const NativeTurnLimit = 4 * time.Minute
const ReceiptReserve = 15 * time.Second
const ExecutionLease = NativeTurnLimit + ReceiptReserve

type Binding struct {
	ExecuteBeforeMS int64  `json:"execute_before_ms,omitempty"`
	Role            string `json:"role"`
	ConfigVersion   int64  `json:"config_version"`
	Persona         string `json:"persona,omitempty"`
	Prompt          string `json:"prompt,omitempty"`
	Kind            string `json:"kind,omitempty"`
	Work            string `json:"work"`
	Agent           string `json:"agent"`
	Owner           string `json:"owner"`
	Epoch           int64  `json:"epoch"`
	Generation      int64  `json:"generation"`
	Session         string `json:"session"`
}
type FailedWrite struct {
	Slot   int    `json:"slot"`
	Tool   string `json:"tool"`
	Status int    `json:"status"`
}
type Result struct {
	FailedWrites   []FailedWrite `json:"failed_writes,omitempty"`
	AppliedVersion int64         `json:"applied_version"`
	Kind           string        `json:"kind"`
	WriteSlots     []int         `json:"write_slots,omitempty"`
	Answer         string        `json:"answer,omitempty"`
	Session        string        `json:"session"`
	Turn           int           `json:"turn"`
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
	return &Client{base: "https://" + u.Host, http: &http.Client{Transport: &http.Transport{TLSClientConfig: certs}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Query the durable execution before submitting or resuming it on the same owner.
func (c *Client) Execute(ctx context.Context, b Binding) (Result, error) {
	if b.ExecuteBeforeMS <= 0 {
		return Result{}, errors.New("missing native execution deadline")
	}
	stop := time.UnixMilli(b.ExecuteBeforeMS)
	if latest := time.Now().Add(NativeTurnLimit); stop.After(latest) {
		stop = latest
		b.ExecuteBeforeMS = stop.UnixMilli()
	}
	ctx, cancel := context.WithDeadline(ctx, stop.Add(10*time.Second))
	defer cancel()
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
	if !time.Now().Before(stop) {
		return Result{}, context.DeadlineExceeded
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
	if resp.StatusCode == http.StatusConflict {
		defer resp.Body.Close()
		var conflict struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&conflict) == nil && conflict.Error == "immutable_work_binding" {
			return Result{}, ErrWorkBindingConflict
		}
		return Result{}, errors.New("runtime owner or generation conflict")
	}
	return decode(resp)
}
func decode(resp *http.Response) (Result, error) {
	defer resp.Body.Close()
	var r Result
	if resp.StatusCode == http.StatusUnprocessableEntity {
		return r, ErrConfiguration
	}
	if resp.StatusCode != 200 {
		return r, errors.New("runtime has no completed execution")
	}
	err := json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&r)
	if err == nil && (r.Session == "" || r.Turn < 0) {
		err = errors.New("invalid runtime completion")
	}
	return r, err
}

func (c *Client) Plan(ctx context.Context, input routing.PlanRequest) (routing.Plan, error) {
	ctx, cancel := context.WithTimeout(ctx, 85*time.Second)
	defer cancel()
	var out routing.Plan
	b, _ := json.Marshal(map[string]any{"request": input, "catalog": routing.PlanningCatalog(), "server_fields": routing.ServerFields()})
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+"/intent", bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return out, errors.New("semantic planner unavailable")
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 96<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&out) != nil || dec.Decode(&struct{}{}) != io.EOF {
		return routing.Plan{}, errors.New("invalid structured runtime plan")
	}
	if err := out.Validate(input); err != nil {
		return routing.Plan{}, err
	}
	return out, nil
}
