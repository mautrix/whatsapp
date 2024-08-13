package main

import (
	_ "go.mau.fi/util/dbutil/litestream"
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

func main() {
	m := mxmain.BridgeMain{
		Name:        "mautrix-whatsapp",
		URL:         "https://github.com/mautrix/whatsapp",
		Description: "A Matrix-WhatsApp puppeting bridge.",
		Version:     "0.10.9",
		Connector:   connector.NewConnector(),
	}
	m.InitVersion(Tag, Commit, BuildTime)
	m.Run()
}
