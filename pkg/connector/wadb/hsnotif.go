// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2025 Tulir Asokan
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

package wadb

import (
	"context"
	"database/sql"
	"errors"

	"go.mau.fi/util/dbutil"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

type HistorySyncNotificationQuery struct {
	BridgeID networkid.BridgeID
	*dbutil.Database
}

const (
	putHSNotificationQuery = `
		INSERT INTO whatsapp_history_sync_notification (bridge_id, user_login_id, data)
		VALUES ($1, $2, $3)
	`
	getNextHSNotificationQuery = `
		SELECT rowid, data FROM whatsapp_history_sync_notification
		WHERE bridge_id=$1 AND user_login_id=$2
	`
	deleteHSNotificationQuery = `
		DELETE FROM whatsapp_history_sync_notification WHERE rowid=$1
	`
)

func (hsnq *HistorySyncNotificationQuery) Put(ctx context.Context, loginID networkid.UserLoginID, notif *waE2E.HistorySyncNotification) error {
	notifBytes, err := proto.Marshal(notif)
	if err != nil {
		return err
	}
	_, err = hsnq.Exec(ctx, putHSNotificationQuery, hsnq.BridgeID, loginID, notifBytes)
	return err
}

func (hsnq *HistorySyncNotificationQuery) GetNext(ctx context.Context, loginID networkid.UserLoginID) (*waE2E.HistorySyncNotification, int, error) {
	var notifBytes []byte
	var rowid int
	err := hsnq.QueryRow(ctx, getNextHSNotificationQuery, hsnq.BridgeID, loginID).Scan(&rowid, &notifBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	} else if err != nil {
		return nil, 0, err
	}
	var notif waE2E.HistorySyncNotification
	if err = proto.Unmarshal(notifBytes, &notif); err != nil {
		return nil, 0, err
	}
	return &notif, rowid, nil
}

func (hsnq *HistorySyncNotificationQuery) Delete(ctx context.Context, rowid int) error {
	_, err := hsnq.Exec(ctx, deleteHSNotificationQuery, rowid)
	return err
}
