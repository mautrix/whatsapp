// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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

package connector

import (
	"context"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type MediaEditCacheKey struct {
	MessageID  networkid.MessageID
	PortalMXID id.RoomID
}

type MediaEditCacheValue struct {
	Part   *bridgev2.ConvertedMessagePart
	Expiry time.Time
}

type MediaEditCache map[MediaEditCacheKey]MediaEditCacheValue

func (wa *WhatsAppConnector) mediaEditCacheExpireLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	ctxDone := ctx.Done()
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-ctxDone:
			return
		}
		wa.expireMediaEditCache()
	}
}

func (wa *WhatsAppConnector) AddMediaEditCache(portal *bridgev2.Portal, messageID networkid.MessageID, converted *bridgev2.ConvertedMessagePart) {
	if converted.Type != event.EventSticker && !converted.Content.MsgType.IsMedia() {
		return
	}
	wa.mediaEditCacheLock.Lock()
	defer wa.mediaEditCacheLock.Unlock()
	wa.mediaEditCache[MediaEditCacheKey{
		MessageID:  messageID,
		PortalMXID: portal.MXID,
	}] = MediaEditCacheValue{
		Part:   converted,
		Expiry: time.Now().Add(EditMaxAge + 5*time.Minute),
	}
}

func (wa *WhatsAppConnector) GetMediaEditCache(portal *bridgev2.Portal, messageID networkid.MessageID) *bridgev2.ConvertedMessagePart {
	wa.mediaEditCacheLock.RLock()
	defer wa.mediaEditCacheLock.RUnlock()
	value, ok := wa.mediaEditCache[MediaEditCacheKey{
		MessageID:  messageID,
		PortalMXID: portal.MXID,
	}]
	if !ok || time.Until(value.Expiry) < 0 {
		return nil
	}
	return value.Part
}

func (wa *WhatsAppConnector) expireMediaEditCache() {
	wa.mediaEditCacheLock.Lock()
	defer wa.mediaEditCacheLock.Unlock()
	for key, value := range wa.mediaEditCache {
		if time.Until(value.Expiry) < 0 {
			delete(wa.mediaEditCache, key)
		}
	}
}
