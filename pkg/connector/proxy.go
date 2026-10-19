package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"maunium.net/go/mautrix"
)

// TODO move proxy stuff to mautrix-go

var getProxyClient = &http.Client{Timeout: 30 * time.Second}

type respGetProxy struct {
	ProxyURL string `json:"proxy_url"`
}

func (wa *WhatsAppConnector) getProxy(ctx context.Context, reason string) (string, error) {
	if wa.Config.GetProxyURL == "" {
		return wa.Config.Proxy, nil
	}
	parsed, err := url.Parse(wa.Config.GetProxyURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse address: %w", err)
	}
	q := parsed.Query()
	q.Set("reason", reason)
	parsed.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", fmt.Errorf("failed to prepare request: %w", err)
	}
	req.Header.Set("User-Agent", mautrix.DefaultUserAgent)
	resp, err := getProxyClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 || resp.StatusCode < 200 {
		return "", fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}
	var respData respGetProxy
	err = json.NewDecoder(resp.Body).Decode(&respData)
	if err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}
	return respData.ProxyURL, nil
}

func (wa *WhatsAppConnector) updateProxy(ctx context.Context, client *whatsmeow.Client, isLogin bool) error {
	if wa.Config.ProxyOnlyLogin && !isLogin {
		return nil
	}
	reason := "connect"
	if isLogin {
		reason = "login"
	}
	if proxy, err := wa.getProxy(ctx, reason); err != nil {
		return fmt.Errorf("failed to get proxy address: %w", err)
	} else if proxy == "" {
		return nil
	} else if err = client.SetProxyAddress(proxy, whatsmeow.SetProxyOptions{
		OnlyLogin: wa.Config.ProxyOnlyLogin,
		NoMedia:   wa.Config.ProxyOnlyLogin,
	}); err != nil {
		return fmt.Errorf("failed to set proxy address: %w", err)
	}
	zerolog.Ctx(ctx).Debug().Msg("Enabled proxy")
	return nil
}
