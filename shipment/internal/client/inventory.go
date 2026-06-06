// Package client is Shipment's gateway to the Inventory service. The
// InventoryClient interface lets the saga be unit-tested with a fake, while the
// production HTTP implementation propagates the distributed trace and forwards
// the caller's Authorization header verbatim (Feature Spec §2.3, plan §5.4).
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	httptrace "github.com/DataDog/dd-trace-go/contrib/net/http/v2"
	"github.com/adamkekesi/microservice-demo/platform/apperror"
)

// ReserveRequest is the body sent to Inventory's POST /reservations.
type ReserveRequest struct {
	WarehouseID    string `json:"warehouse_id"`
	ItemID         string `json:"item_id"`
	Quantity       int    `json:"quantity"`
	IdempotencyKey string `json:"idempotency_key"`
}

// Reservation is the subset of Inventory's reservation we care about.
type Reservation struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// InventoryClient is the contract Shipment depends on. Methods return nil on
// success or an *apperror.AppError: a DEPENDENCY_UNAVAILABLE (502) for
// transport/5xx failures, or a mirror of Inventory's structured 4xx error
// (code/status/details preserved) so the saga can branch on it.
type InventoryClient interface {
	Reserve(ctx context.Context, authorization string, req ReserveRequest) (*Reservation, error)
	Commit(ctx context.Context, authorization, reservationID string) error
	Release(ctx context.Context, authorization, reservationID string) error
}

// HTTPClient is the production InventoryClient over HTTP.
type HTTPClient struct {
	baseURL string
	http    *http.Client
}

// NewHTTPClient builds a traced HTTP client (trace context is injected into
// outbound headers so Inventory continues the same trace).
func NewHTTPClient(baseURL string, timeout time.Duration) *HTTPClient {
	return &HTTPClient{
		baseURL: baseURL,
		http:    httptrace.WrapClient(&http.Client{Timeout: timeout}),
	}
}

func (c *HTTPClient) Reserve(ctx context.Context, authorization string, req ReserveRequest) (*Reservation, error) {
	var out Reservation
	if err := c.do(ctx, http.MethodPost, "/reservations", authorization, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *HTTPClient) Commit(ctx context.Context, authorization, reservationID string) error {
	return c.do(ctx, http.MethodPost, "/reservations/"+reservationID+"/commit", authorization, nil, nil)
}

func (c *HTTPClient) Release(ctx context.Context, authorization, reservationID string) error {
	return c.do(ctx, http.MethodPost, "/reservations/"+reservationID+"/release", authorization, nil, nil)
}

// do performs the request and maps the response to nil / *apperror.AppError.
func (c *HTTPClient) do(ctx context.Context, method, path, authorization string, body, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return apperror.Internal("encode inventory request").Wrap(err)
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return apperror.Internal("build inventory request").Wrap(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization) // forwarded verbatim
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return apperror.DependencyUnavailable("inventory service is unreachable").Wrap(err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if out != nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, out); err != nil {
				return apperror.DependencyUnavailable("inventory returned an undecodable response").Wrap(err)
			}
		}
		return nil
	case resp.StatusCode >= 500:
		return apperror.DependencyUnavailable(fmt.Sprintf("inventory returned status %d", resp.StatusCode))
	default:
		return inventoryErrorFromBody(resp.StatusCode, raw)
	}
}

// inventoryErrorFromBody mirrors Inventory's standard error envelope into an
// AppError so the saga can branch on the code and pass details through.
func inventoryErrorFromBody(status int, raw []byte) error {
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Error.Code == "" {
		return apperror.DependencyUnavailable(fmt.Sprintf("inventory returned status %d", status))
	}
	return (&apperror.AppError{
		Status:  status,
		Code:    env.Error.Code,
		Message: env.Error.Message,
		Details: env.Error.Details,
	})
}
