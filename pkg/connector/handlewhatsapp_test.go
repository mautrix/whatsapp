// Copyright (c) 2026 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package connector

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestSyncContactGhostCanBeDisabled(t *testing.T) {
	wa := &WhatsAppClient{
		Main: &WhatsAppConnector{Config: Config{DisableContactSync: true}},
		JID:  types.JID{User: "self", Server: types.DefaultUserServer},
	}
	wa.syncContactGhost(types.JID{User: "contact", Server: types.DefaultUserServer}, "test", nil)
}
