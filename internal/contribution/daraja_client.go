package contribution

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DarajaSTKInitiator is the smallest provider boundary needed by contribution
// initiation. Without it, tests and the service would have to construct real
// Safaricom HTTP requests to prove payment-request persistence.
type DarajaSTKInitiator interface {
	InitiateSTK(ctx context.Context, request DarajaSTKInitiationRequest) (string, error)
}

type DarajaSTKInitiationRequest struct {
	PhoneNumber string
	Amount      int64
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
	httpClient *http.Client
	config     DarajaClientConfig
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
	token, err := c.accessToken(ctx)
	if err != nil {
		return "", err
	}

	timestamp := time.Now().Format("20060102150405")
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
		"CallBackURL":       c.config.CallbackURL,
		"AccountReference":  c.config.AccountReference,
		"TransactionDesc":   c.config.TransactionDesc,
	})
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.config.BaseURL, "/")+"/mpesa/stkpush/v1/processrequest", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var payload struct {
		CheckoutRequestID string `json:"CheckoutRequestID"`
		ResponseCode      string `json:"ResponseCode"`
		ErrorCode         string `json:"errorCode"`
		ErrorMessage      string `json:"errorMessage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || payload.ResponseCode != "0" {
		return "", fmt.Errorf("daraja stk rejected: %s %s", payload.ErrorCode, payload.ErrorMessage)
	}
	if strings.TrimSpace(payload.CheckoutRequestID) == "" {
		return "", fmt.Errorf("daraja stk response missing checkout request id")
	}
	return strings.TrimSpace(payload.CheckoutRequestID), nil
}

// accessToken requests a fresh Daraja bearer token for one payment initiation.
// Removing it would force callers to pass provider tokens around or add shared
// token cache state before the code needs it.
func (c *DarajaClient) accessToken(ctx context.Context) (string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.config.BaseURL, "/")+"/oauth/v1/generate?grant_type=client_credentials", nil)
	if err != nil {
		return "", err
	}
	httpReq.SetBasicAuth(c.config.ConsumerKey, c.config.ConsumerSecret)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var payload struct {
		AccessToken string `json:"access_token"`
		ErrorCode   string `json:"errorCode"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || strings.TrimSpace(payload.AccessToken) == "" {
		return "", fmt.Errorf("daraja token rejected: %s %s", payload.ErrorCode, payload.Error)
	}
	return strings.TrimSpace(payload.AccessToken), nil
}
