package voip

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const FocusWellKnownKey = "org.matrix.msc4143.rtc_foci"

var ErrNoLiveKitFocus = errors.New("voip: no LiveKit MatrixRTC focus found")

type Focus struct {
	Type              string `json:"type"`
	LiveKitServiceURL string `json:"livekit_service_url"`
}

type WellKnownClient struct {
	RTCFoci []Focus `json:"org.matrix.msc4143.rtc_foci"`
}

func DiscoverLiveKitFocus(ctx context.Context, httpClient *http.Client, serverName, overrideURL string) (*Focus, error) {
	if overrideURL != "" {
		if err := validateHTTPSURL(overrideURL); err != nil {
			return nil, fmt.Errorf("invalid configured livekit service URL: %w", err)
		}
		return &Focus{Type: "livekit", LiveKitServiceURL: overrideURL}, nil
	}
	if serverName == "" {
		return nil, fmt.Errorf("matrix server name is required")
	}
	if strings.Contains(serverName, "://") {
		return nil, fmt.Errorf("matrix server name must not include a scheme")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	wellKnownURL := "https://" + serverName + "/.well-known/matrix/client"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnownURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Matrix client well-known: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Matrix client well-known returned HTTP %d", resp.StatusCode)
	}
	var wellKnown WellKnownClient
	if err = json.NewDecoder(resp.Body).Decode(&wellKnown); err != nil {
		return nil, fmt.Errorf("failed to decode Matrix client well-known: %w", err)
	}
	for _, focus := range wellKnown.RTCFoci {
		if focus.Type != "livekit" || focus.LiveKitServiceURL == "" {
			continue
		}
		if err = validateHTTPSURL(focus.LiveKitServiceURL); err != nil {
			return nil, fmt.Errorf("invalid livekit focus URL in well-known: %w", err)
		}
		return &focus, nil
	}
	return nil, ErrNoLiveKitFocus
}

type MatrixOpenIDToken struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	MatrixServerName string `json:"matrix_server_name"`
	ExpiresIn        int    `json:"expires_in"`
}

type LiveKitAuthRequest struct {
	RoomID        string             `json:"room_id,omitempty"`
	SlotID        string             `json:"slot_id,omitempty"`
	OpenIDToken   MatrixOpenIDToken  `json:"openid_token"`
	Member        *LiveKitAuthMember `json:"member,omitempty"`
	DeviceID      string             `json:"device_id,omitempty"`
	SessionID     string             `json:"session_id,omitempty"`
	ParticipantID string             `json:"participant_id,omitempty"`
	FocusType     string             `json:"focus_type,omitempty"`
	Extra         map[string]any     `json:"extra,omitempty"`
}

type LegacyLiveKitAuthRequest struct {
	Room        string            `json:"room"`
	OpenIDToken MatrixOpenIDToken `json:"openid_token"`
	DeviceID    string            `json:"device_id"`
}

type LiveKitAuthMember struct {
	ID              string `json:"id,omitempty"`
	ClaimedDeviceID string `json:"claimed_device_id,omitempty"`
	ClaimedUserID   string `json:"claimed_user_id,omitempty"`
}

type LiveKitAuthResponse struct {
	URL      string `json:"url,omitempty"`
	Token    string `json:"token,omitempty"`
	JWTToken string `json:"jwt,omitempty"`
	RoomName string `json:"room,omitempty"`

	ServerURL   string `json:"server_url,omitempty"`
	LiveKitURL  string `json:"livekit_url,omitempty"`
	AccessToken string `json:"access_token,omitempty"`
}

func (resp LiveKitAuthResponse) ConnectionURL() string {
	for _, candidate := range []string{resp.URL, resp.ServerURL, resp.LiveKitURL} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func (resp LiveKitAuthResponse) JWT() string {
	for _, candidate := range []string{resp.Token, resp.JWTToken, resp.AccessToken} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}

func RequestLiveKitAuth(ctx context.Context, httpClient *http.Client, liveKitServiceURL string, authReq LiveKitAuthRequest) (*LiveKitAuthResponse, error) {
	if err := validateHTTPSURL(liveKitServiceURL); err != nil {
		return nil, fmt.Errorf("invalid livekit service URL: %w", err)
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	body, err := json.Marshal(authReq)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for _, endpoint := range liveKitAuthEndpoints(liveKitServiceURL) {
		resp, err := postLiveKitAuth(ctx, httpClient, endpoint, body)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.ConnectionURL() == "" || resp.JWT() == "" {
			return nil, fmt.Errorf("livekit auth response did not include both URL and token")
		}
		return resp, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("livekit auth did not try any endpoints")
}

func RequestLegacyLiveKitAuth(ctx context.Context, httpClient *http.Client, liveKitServiceURL string, authReq LegacyLiveKitAuthRequest) (*LiveKitAuthResponse, error) {
	if err := validateHTTPSURL(liveKitServiceURL); err != nil {
		return nil, fmt.Errorf("invalid livekit service URL: %w", err)
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	body, err := json.Marshal(authReq)
	if err != nil {
		return nil, err
	}
	resp, err := postLiveKitAuth(ctx, httpClient, legacyLiveKitAuthEndpoint(liveKitServiceURL), body)
	if err != nil {
		return nil, err
	}
	if resp.ConnectionURL() == "" || resp.JWT() == "" {
		return nil, fmt.Errorf("livekit auth response did not include both URL and token")
	}
	return resp, nil
}

func liveKitAuthEndpoints(rawURL string) []string {
	trimmed := strings.TrimRight(rawURL, "/")
	if strings.HasSuffix(trimmed, "/get_token") || strings.HasSuffix(trimmed, "/sfu/get") {
		return []string{trimmed}
	}
	return []string{
		trimmed + "/get_token",
		trimmed + "/sfu/get",
		trimmed,
	}
}

func legacyLiveKitAuthEndpoint(rawURL string) string {
	trimmed := strings.TrimRight(rawURL, "/")
	if strings.HasSuffix(trimmed, "/sfu/get") {
		return trimmed
	}
	if strings.HasSuffix(trimmed, "/get_token") {
		return strings.TrimSuffix(trimmed, "/get_token") + "/sfu/get"
	}
	return trimmed + "/sfu/get"
}

func postLiveKitAuth(ctx context.Context, httpClient *http.Client, liveKitServiceURL string, body []byte) (*LiveKitAuthResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, liveKitServiceURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request livekit token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("livekit auth returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var authResp LiveKitAuthResponse
	if err = json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return nil, fmt.Errorf("failed to decode livekit auth response: %w", err)
	}
	return &authResp, nil
}

func validateHTTPSURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" && parsed.Scheme != "wss" {
		return fmt.Errorf("URL must use https or wss")
	}
	if parsed.Host == "" {
		return fmt.Errorf("URL must include a host")
	}
	return nil
}
