//go:build amd64 && cgo && !noplugin

package main

import (
	"fmt"
	"os"
	"plugin"

	"go.mau.fi/util/exerrors"

	"go.mau.fi/mautrix-whatsapp/pkg/connector"
)

func init() {
	path := os.Getenv("WM_PLUGIN_PATH")
	if path == "" {
		return
	}
	fmt.Println("Loading plugin from", path)
	plug := exerrors.Must(plugin.Open(path))
	sym := exerrors.Must(plug.Lookup("NewClient"))
	connector.NewMC = sym.(connector.NewMCFunc)
}
