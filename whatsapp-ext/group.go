// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2019 Tulir Asokan
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

package whatsappExt

import (
	"encoding/json"
	"fmt"

	"maunium.net/go/mautrix-whatsapp/types"
)

type CreateGroupResponse struct {
	Status       int              `json:"status"`
	GroupID      types.WhatsAppID `json:"gid"`
	Participants map[types.WhatsAppID]struct {
		Code string `json:"code"`
	} `json:"participants"`

	Source string `json:"-"`
}

type actualCreateGroupResponse struct {
	Status       int              `json:"status"`
	GroupID      types.WhatsAppID `json:"gid"`
	Participants []map[types.WhatsAppID]struct {
		Code string `json:"code"`
	} `json:"participants"`
}

func (ext *ExtendedConn) CreateGroup(subject string, participants []types.WhatsAppID) (*CreateGroupResponse, error) {
	respChan, err := ext.Conn.CreateGroup(subject, participants)
	if err != nil {
		return nil, err
	}
	var resp CreateGroupResponse
	var actualResp actualCreateGroupResponse
	resp.Source = <-respChan
	fmt.Println(">>>>>>", resp.Source)
	err = json.Unmarshal([]byte(resp.Source), &actualResp)
	if err != nil {
		return nil, err
	}
	resp.Status = actualResp.Status
	resp.GroupID = actualResp.GroupID
	resp.Participants = make(map[types.WhatsAppID]struct {
		Code string `json:"code"`
	})
	for _, participantMap := range actualResp.Participants {
		for jid, status := range participantMap {
			resp.Participants[jid] = status
		}
	}
	return &resp, nil
}
