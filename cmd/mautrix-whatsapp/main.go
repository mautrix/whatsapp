package main

import (
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"

	"maunium.net/go/mautrix-whatsapp/pkg/connector"
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
	Version:     "0.11.0",
	Connector:   c,
}

func main() {
	m.PostInit = func() {
		m.CheckLegacyDB(
			57,
			"v0.8.6",
			"v0.11.0",
			m.LegacyMigrateSimple(legacyMigrateRenameTables, legacyMigrateCopyData, 16),
			true,
		)
	}
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
