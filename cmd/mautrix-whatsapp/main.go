package main

import (
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
	Version:     "0.12.4",
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
				legacyMigrateRenameTables, legacyMigrateCopyData, 21,
				upgrades.Table, "whatsapp_version", 5,
			),
			true,
		)
	}
	m.PostStart = func() {
		if m.Matrix.Provisioning != nil {
			m.Matrix.Provisioning.Router.HandleFunc("GET /v1/login", legacyProvLogin)
			m.Matrix.Provisioning.Router.HandleFunc("POST /v1/logout", legacyProvLogout)
			m.Matrix.Provisioning.Router.HandleFunc("GET /v1/contacts", legacyProvContacts)
			m.Matrix.Provisioning.Router.HandleFunc("GET /v1/resolve_identifier/{number}", legacyProvResolveIdentifier)
			m.Matrix.Provisioning.Router.HandleFunc("POST /v1/pm/{number}", legacyProvResolveIdentifier)
			m.Matrix.Provisioning.GetAuthFromRequest = legacyProvAuth
		}
	}
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
