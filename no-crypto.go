// +build !cgo nocrypto

package main

import (
	"errors"
)

func NewCryptoHelper(bridge *Bridge) Crypto {
	if !bridge.Config.Bridge.Encryption.Allow {
		bridge.Log.Warnln("Bridge built without end-to-bridge encryption, but encryption is enabled in config")
	}
	bridge.Log.Debugln("Bridge built without end-to-bridge encryption")
	return nil
}

var NoSessionFound = errors.New("nil")
