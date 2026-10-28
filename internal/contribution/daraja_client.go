package contribution

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DarajaSTKInitiator is the smallest provider boundary needed by contribution
// initiation. Without it, tests and the service would have to construct real
// Safaricom HTTP requests to prove payment-request persistence.
type DarajaSTKInitiator interface {
	InitiateSTK(ctx context.Context, request DarajaSTKInitiationRequest) (string, error)
}

type DarajaSTKInitiationRequest struct {
	IdempotencyKey string
	PhoneNumber    string
	Amount         int64
}

type DarajaClientConfig struct {
	BaseURL           string
	BusinessShortCode string
	Passkey           string
	CallbackURL       string
	AccountReference  string
	TransactionDesc   string
	ConsumerKey       string
	ConsumerSecret    string
}

type DarajaClient struct {
	httpClient     *http.Client
	config         DarajaClientConfig
	tokenMu        sync.Mutex
	token          string
	tokenExpiresAt time.Time
	queryAfter     time.Time
}

// QuerySTK checks an existing checkout without issuing another debit. Without
// it, a lost callback leaves the request permanently unconfirmed.
func (c *DarajaClient) QuerySTK(ctx context.Context, checkoutID string) (string, string, error) {
	if c == nil {
		return "", "", ErrDarajaClientMissing
	}
	token, err := c.accessToken(ctx)
	if err != nil {
		return "", "", err
	}
	timestamp := time.Now().UTC().Add(3 * time.Hour).Format("20060102150405")
	body, err := json.Marshal(map[string]string{
		"BusinessShortCode": c.config.BusinessShortCode,
		"Password":          base64.StdEncoding.EncodeToString([]byte(c.config.BusinessShortCode + c.config.Passkey + timestamp)),
		"Timestamp":         timestamp, "CheckoutRequestID": checkoutID,
	})
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.config.BaseURL, "/")+"/mpesa/stkpushquery/v1/query", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Error("failed to close response body")
		}
	}()
	var result struct {
		CheckoutRequestID string          `json:"CheckoutRequestID"`
		ResultCode        json.RawMessage `json:"ResultCode"`
		ResultDesc        string          `json:"ResultDesc"`
		ErrorCode         string          `json:"errorCode"`
	}
	if resp.StatusCode == http.StatusUnauthorized {
		c.tokenMu.Lock()
		if c.token == token {
			c.token = ""
		}
		c.tokenMu.Unlock()
	}
	// The query worker pauses all checkouts after provider failures.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 || resp.StatusCode == http.StatusForbidden {
		c.queryAfter = time.Now().Add(time.Minute)
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
			c.queryAfter = time.Now().Add(time.Duration(seconds) * time.Second)
		} else if retryAt, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil && retryAt.After(c.queryAfter) {
			c.queryAfter = retryAt
		}
	}
	if resp.StatusCode != http.StatusOK {
		_ = json.NewDecoder(resp.Body).Decode(&result)
		return "", "", fmt.Errorf("daraja query rejected (HTTP %d, content-type %q, error_code %q)", resp.StatusCode, resp.Header.Get("Content-Type"), result.ErrorCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", fmt.Errorf("daraja query response (HTTP %d, content-type %q): %w", resp.StatusCode, resp.Header.Get("Content-Type"), err)
	}
	if resp.StatusCode != http.StatusOK || result.CheckoutRequestID != checkoutID || len(result.ResultCode) == 0 {
		return "", "", fmt.Errorf("daraja query returned no verified checkout result (HTTP %d, error_code %q, checkout_matches %t, result_present %t)", resp.StatusCode, result.ErrorCode, result.CheckoutRequestID == checkoutID, len(result.ResultCode) != 0)
	}
	return strings.Trim(string(result.ResultCode), "\""), result.ResultDesc, nil
}

// NewDarajaClient keeps provider configuration at the application edge.
// Without this constructor, Daraja credentials would be pulled from global
// state inside the contribution service.
func NewDarajaClient(httpClient *http.Client, cfg DarajaClientConfig) *DarajaClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &DarajaClient{httpClient: httpClient, config: cfg}
}

// InitiateSTK performs Safaricom's OAuth + STK push sequence. Without this
// method, the contribution service could persist a payment request but never
// create the M-Pesa prompt that produces the callback it later expects.
func (c *DarajaClient) InitiateSTK(ctx context.Context, request DarajaSTKInitiationRequest) (string, error) {
	callbackURL, err := url.Parse(c.config.CallbackURL)
	if err != nil {
		return "", fmt.Errorf("%w: invalid daraja callback URL: %v", ErrDarajaNotSent, err)
	}
	// A signed request key lets callbacks recover a lost initiation response without guessing a payer.
	if request.IdempotencyKey != "" {
		mac := hmac.New(sha256.New, []byte(c.config.Passkey))
		_, _ = mac.Write([]byte("daraja-callback:" + request.IdempotencyKey))
		callbackURL.Path = strings.TrimRight(callbackURL.Path, "/") + "/" + request.IdempotencyKey + "/" + fmt.Sprintf("%x", mac.Sum(nil))
	}
	token, err := c.accessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDarajaNotSent, err)
	}

	timestamp := time.Now().UTC().Add(3 * time.Hour).Format("20060102150405")
	password := base64.StdEncoding.EncodeToString([]byte(c.config.BusinessShortCode + c.config.Passkey + timestamp))
	body, err := json.Marshal(map[string]any{
		"BusinessShortCode": c.config.BusinessShortCode,
		"Password":          password,
		"Timestamp":         timestamp,
		"TransactionType":   "CustomerPayBillOnline",
		"Amount":            request.Amount,
		"PartyA":            request.PhoneNumber,
		"PartyB":            c.config.BusinessShortCode,
		"PhoneNumber":       request.PhoneNumber,
		"CallBackURL":       callbackURL.String(),
		"AccountReference":  c.config.AccountReference,
		"TransactionDesc":   c.config.TransactionDesc,
	})
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.config.BaseURL, "/")+"/mpesa/stkpush/v1/processrequest", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDarajaNotSent, err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("failed to close response body", "error", err)
		}
	}()
	var payload struct {
		CheckoutRequestID string `json:"CheckoutRequestID"`
		ResponseCode      string `json:"ResponseCode"`
		ErrorCode         string `json:"errorCode"`
		ErrorMessage      string `json:"errorMessage"`
	}
	if resp.StatusCode == http.StatusUnauthorized {
		c.tokenMu.Lock()
		if c.token == token {
			c.token = ""
		}
		c.tokenMu.Unlock()
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("daraja initiation response (HTTP %d, content-type %q): %w", resp.StatusCode, resp.Header.Get("Content-Type"), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || payload.ResponseCode != "0" {
		// A provider outage or rate limit cannot prove that no prompt was sent.
		if resp.StatusCode == http.StatusBadRequest && payload.ErrorCode != "" {
			return "", ErrDarajaRejected
		}
		return "", fmt.Errorf("daraja stk rejected: %s %s", payload.ErrorCode, payload.ErrorMessage)
	}
	if strings.TrimSpace(payload.CheckoutRequestID) == "" {
		return "", fmt.Errorf("daraja stk response missing checkout request id")
	}
	return strings.TrimSpace(payload.CheckoutRequestID), nil
}

// accessToken shares unexpired tokens and serializes refreshes to avoid flooding OAuth.
func (c *DarajaClient) accessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExpiresAt) {
		return c.token, nil
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.config.BaseURL, "/")+"/oauth/v1/generate?grant_type=client_credentials", nil)
	if err != nil {
		return "", err
	}
	httpReq.SetBasicAuth(c.config.ConsumerKey, c.config.ConsumerSecret)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("failed to close response body", "error", err)
		}
	}()
	var payload struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
		ErrorCode   string          `json:"errorCode"`
		Error       string          `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("daraja token response (HTTP %d, content-type %q): %w", resp.StatusCode, resp.Header.Get("Content-Type"), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || strings.TrimSpace(payload.AccessToken) == "" {
		return "", fmt.Errorf("daraja token rejected: %s %s", payload.ErrorCode, payload.Error)
	}
	c.token = strings.TrimSpace(payload.AccessToken)
	seconds, _ := strconv.ParseInt(strings.Trim(string(payload.ExpiresIn), "\""), 10, 64)
	c.tokenExpiresAt = time.Now().Add(time.Duration(seconds)*time.Second - time.Minute)
	return c.token, nil
}
