package connector

import (
	"context"
	"fmt"
	"net/http"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
)

func (wa *WhatsAppConnector) SupportsProxy() {}

func (wa *WhatsAppConnector) updateProxy(ctx context.Context, client *whatsmeow.Client, isLogin bool) error {
	var proxy string
	if !wa.Config.ProxyOnlyLogin || isLogin {
		reason := "connect"
		if isLogin {
			reason = "login"
		}
		var err error
		proxy, err = wa.Bridge.GetProxy(ctx, reason)
		if err != nil {
			return fmt.Errorf("failed to get proxy address: %w", err)
		}
	}
	settings := wa.Bridge.GetHTTPClientSettings().WithGlobalTimeout(0)
	if settings.HTTPProxy == nil {
		settings.HTTPProxy = http.ProxyFromEnvironment
	}
	proxySettings := settings
	if proxy != "" {
		var err error
		proxySettings, err = settings.WithProxy(proxy)
		if err != nil {
			return fmt.Errorf("failed to set proxy address: %w", err)
		}
	}
	client.SetPreLoginHTTPClient(proxySettings.Compile())
	if wa.Config.ProxyOnlyLogin {
		proxySettings = settings
	}
	client.SetWebsocketHTTPClient(proxySettings.Compile())
	client.SetMediaHTTPClient(proxySettings.WithResponseHeaderTimeout(0).Compile())
	if proxy != "" {
		zerolog.Ctx(ctx).Debug().Msg("Enabled proxy")
	}
	return nil
}
