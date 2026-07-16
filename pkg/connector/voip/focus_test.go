package voip

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscoverLiveKitFocusOverride(t *testing.T) {
	focus, err := DiscoverLiveKitFocus(context.Background(), nil, "", "https://rtc.example.com/livekit/jwt")
	if err != nil {
		t.Fatalf("DiscoverLiveKitFocus returned error: %v", err)
	}
	if focus.Type != "livekit" || focus.LiveKitServiceURL != "https://rtc.example.com/livekit/jwt" {
		t.Fatalf("unexpected focus: %+v", focus)
	}
}

func TestRequestLiveKitAuthAcceptsResponseAliases(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/get_token" {
			t.Fatalf("path = %q, want /get_token", r.URL.Path)
		}
		var req LiveKitAuthRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.OpenIDToken.AccessToken != "openid" {
			t.Fatalf("OpenID token = %q, want openid", req.OpenIDToken.AccessToken)
		}
		_ = json.NewEncoder(w).Encode(LiveKitAuthResponse{
			ServerURL: "wss://livekit.example.com",
			JWTToken:  "jwt",
		})
	}))
	defer server.Close()

	resp, err := RequestLiveKitAuth(context.Background(), server.Client(), server.URL, LiveKitAuthRequest{
		OpenIDToken: MatrixOpenIDToken{AccessToken: "openid"},
	})
	if err != nil {
		t.Fatalf("RequestLiveKitAuth returned error: %v", err)
	}
	if resp.ConnectionURL() != "wss://livekit.example.com" || resp.JWT() != "jwt" {
		t.Fatalf("unexpected response aliases: %+v", resp)
	}
}

func TestRequestLegacyLiveKitAuthUsesSFUEndpoint(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sfu/get" {
			t.Fatalf("path = %q, want /sfu/get", r.URL.Path)
		}
		var req LegacyLiveKitAuthRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.Room != "!room:example.com" || req.DeviceID != "DEVICE" {
			t.Fatalf("unexpected legacy request: %+v", req)
		}
		_ = json.NewEncoder(w).Encode(LiveKitAuthResponse{
			URL:   "wss://livekit.example.com",
			Token: "jwt",
		})
	}))
	defer server.Close()

	resp, err := RequestLegacyLiveKitAuth(context.Background(), server.Client(), server.URL, LegacyLiveKitAuthRequest{
		Room:        "!room:example.com",
		DeviceID:    "DEVICE",
		OpenIDToken: MatrixOpenIDToken{AccessToken: "openid", MatrixServerName: "example.com"},
	})
	if err != nil {
		t.Fatalf("RequestLegacyLiveKitAuth returned error: %v", err)
	}
	if resp.ConnectionURL() != "wss://livekit.example.com" || resp.JWT() != "jwt" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestLiveKitAuthEndpoints(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want []string
	}{
		{
			name: "base",
			url:  "https://rtc.example.com/livekit/jwt",
			want: []string{
				"https://rtc.example.com/livekit/jwt/get_token",
				"https://rtc.example.com/livekit/jwt/sfu/get",
				"https://rtc.example.com/livekit/jwt",
			},
		},
		{
			name: "explicit",
			url:  "https://rtc.example.com/livekit/jwt/sfu/get",
			want: []string{"https://rtc.example.com/livekit/jwt/sfu/get"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := liveKitAuthEndpoints(test.url)
			if len(got) != len(test.want) {
				t.Fatalf("got %d endpoints, want %d: %+v", len(got), len(test.want), got)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("endpoint %d = %q, want %q", i, got[i], test.want[i])
				}
			}
		})
	}
}

func TestLegacyLiveKitAuthEndpoint(t *testing.T) {
	tests := map[string]string{
		"https://rtc.example.com/livekit/jwt":           "https://rtc.example.com/livekit/jwt/sfu/get",
		"https://rtc.example.com/livekit/jwt/":          "https://rtc.example.com/livekit/jwt/sfu/get",
		"https://rtc.example.com/livekit/jwt/get_token": "https://rtc.example.com/livekit/jwt/sfu/get",
		"https://rtc.example.com/livekit/jwt/sfu/get":   "https://rtc.example.com/livekit/jwt/sfu/get",
	}
	for input, want := range tests {
		if got := legacyLiveKitAuthEndpoint(input); got != want {
			t.Fatalf("legacy endpoint for %q = %q, want %q", input, got, want)
		}
	}
}
