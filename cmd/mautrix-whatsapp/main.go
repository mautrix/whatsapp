package main

import (
	"net/http"

	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"go.mau.fi/mautrix-whatsapp/pkg/connector"
	"go.mau.fi/mautrix-whatsapp/pkg/connector/wadb/upgrades"
)

// Information to find out exactly which commit the bridge was built from.
// These are filled at build time with the -X linker flag.
var (
	Tag       = "unknown"
	Commit    = "unknown"
	BuildTime = "unknown"
)

var c = &connector.WhatsAppConnector{}
var m = mxmain.BridgeMain{
	Name:        "mautrix-whatsapp",
	URL:         "https://github.com/mautrix/whatsapp",
	Description: "A Matrix-WhatsApp puppeting bridge.",
	Version:     "0.12.0",
	Connector:   c,
}

func main() {
	bridgeconfig.HackyMigrateLegacyNetworkConfig = migrateLegacyConfig
	m.PostInit = func() {
		m.CheckLegacyDB(
			57,
			"v0.8.6",
			"v0.11.0",
			m.LegacyMigrateWithAnotherUpgrader(
				legacyMigrateRenameTables, legacyMigrateCopyData, 17,
				upgrades.Table, "whatsapp_version", 3,
			),
			true,
		)
	}
	m.PostStart = func() {
		if m.Matrix.Provisioning != nil {
			m.Matrix.Provisioning.Router.HandleFunc("/v1/login", legacyProvLogin).Methods(http.MethodGet)
			m.Matrix.Provisioning.Router.HandleFunc("/v1/logout", legacyProvLogout).Methods(http.MethodPost)
			m.Matrix.Provisioning.Router.HandleFunc("/v1/contacts", legacyProvContacts).Methods(http.MethodGet)
			m.Matrix.Provisioning.Router.HandleFunc("/v1/resolve_identifier/{number}", legacyProvResolveIdentifier).Methods(http.MethodGet)
			m.Matrix.Provisioning.Router.HandleFunc("/v1/pm/{number}", legacyProvResolveIdentifier).Methods(http.MethodPost)
			m.Matrix.Provisioning.GetAuthFromRequest = legacyProvAuth
		}
	}
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
