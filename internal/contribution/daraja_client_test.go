package contribution

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDarajaTokenReuseAndRefresh(t *testing.T) {
	var tokens atomic.Int32
	var unauthorized atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/v1/generate":
			fmt.Fprintf(w, `{"access_token":"token-%d","expires_in":"3600"}`, tokens.Add(1))
		case "/mpesa/stkpush/v1/processrequest":
			if r.Header.Get("Authorization") != fmt.Sprintf("Bearer token-%d", tokens.Load()) {
				t.Error("STK request used wrong token")
			}
			fmt.Fprint(w, `{"ResponseCode":"0","CheckoutRequestID":"checkout"}`)
		case "/mpesa/stkpushquery/v1/query":
			if unauthorized.Swap(false) {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"ResultCode":"0","CheckoutRequestID":"checkout"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewDarajaClient(server.Client(), DarajaClientConfig{BaseURL: server.URL, CallbackURL: "https://example.com/webhooks/daraja/stk"})
	if _, err := client.InitiateSTK(t.Context(), DarajaSTKInitiationRequest{Amount: 100}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.QuerySTK(t.Context(), "checkout"); err != nil {
		t.Fatal(err)
	}
	if tokens.Load() != 1 {
		t.Fatalf("initiation and query should share one token, got %d", tokens.Load())
	}
	client.tokenExpiresAt = time.Now().Add(-time.Second)
	if _, _, err := client.QuerySTK(t.Context(), "checkout"); err != nil {
		t.Fatal(err)
	}
	if tokens.Load() != 2 {
		t.Fatalf("expired token was not refreshed: %d", tokens.Load())
	}
	unauthorized.Store(true)
	if _, _, err := client.QuerySTK(t.Context(), "checkout"); err == nil {
		t.Fatal("unauthorized response must not confirm payment")
	}
	if _, _, err := client.QuerySTK(t.Context(), "checkout"); err != nil {
		t.Fatal(err)
	}
	if tokens.Load() != 3 {
		t.Fatalf("rejected token was reused: %d", tokens.Load())
	}
}

func TestDarajaInitiationFailureClassification(t *testing.T) {
	for _, oauthFails := range []bool{true, false} {
		t.Run(fmt.Sprintf("oauth_failure_%t", oauthFails), func(t *testing.T) {
			var prompts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/v1/generate" {
					if oauthFails {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					fmt.Fprint(w, `{"access_token":"token","expires_in":3600}`)
					return
				}
				prompts.Add(1)
				http.Error(w, "upstream failed after receiving STK", http.StatusBadGateway)
			}))
			defer server.Close()
			client := NewDarajaClient(server.Client(), DarajaClientConfig{BaseURL: server.URL, CallbackURL: "https://example.com/webhooks/daraja/stk"})
			_, err := client.InitiateSTK(t.Context(), DarajaSTKInitiationRequest{Amount: 100})
			if err == nil || errors.Is(err, ErrDarajaNotSent) != oauthFails {
				t.Fatalf("only pre-STK failures permit resend: %v", err)
			}
			if oauthFails && prompts.Load() != 0 {
				t.Fatal("STK was called despite OAuth failure")
			}
		})
	}
}

func TestDarajaQueryFailureBackoff(t *testing.T) {
	for _, status := range []int{429, 500, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/oauth/v1/generate" {
					fmt.Fprint(w, `{"access_token":"token","expires_in":3600}`)
					return
				}
				w.Header().Set("Retry-After", "120")
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(status)
				fmt.Fprint(w, "<html>provider rejected request</html>")
			}))
			defer server.Close()
			client := NewDarajaClient(server.Client(), DarajaClientConfig{BaseURL: server.URL})
			_, _, err := client.QuerySTK(t.Context(), "checkout")
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) || strings.Contains(err.Error(), "invalid character") {
				t.Fatalf("must report HTTP rejection, not JSON failure: %v", err)
			}
			if time.Until(client.queryAfter) < 119*time.Second {
				t.Fatalf("provider Retry-After was ignored: %v", client.queryAfter)
			}
		})
	}
}
