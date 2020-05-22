// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2020 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

// +build !cgo

package main

import (
	"image"
	"io"

	"golang.org/x/image/webp"
)

func NewCryptoHelper(bridge *Bridge) Crypto {
	if !bridge.Config.Bridge.Encryption.Allow {
		bridge.Log.Warnln("Bridge built without end-to-bridge encryption, but encryption is enabled in config")
	}
	bridge.Log.Debugln("Bridge built without end-to-bridge encryption")
	return nil
}

func decodeWebp(r io.Reader) (image.Image, error) {
	return webp.Decode(r)
}
