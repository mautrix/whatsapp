// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2018 Tulir Asokan
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

package whatsapp_ext

import (
	"fmt"
	"encoding/json"
	"github.com/Rhymen/go-whatsapp"
	"net/http"
	"io/ioutil"
	"io"
	"strings"
)

type ExtendedConn struct {
	*whatsapp.Conn
}

func ExtendConn(conn *whatsapp.Conn) *ExtendedConn {
	return &ExtendedConn{
		Conn: conn,
	}
}

type GroupInfo struct {
	JID      string `json:"jid"`
	OwnerJID string `json:"owner"`

	Name        string `json:"subject"`
	NameSetTime int64  `json:"subjectTime"`
	NameSetBy   string `json:"subjectOwner"`

	Topic      string `json:"desc"`
	TopicID    string `json:"descId"`
	TopicSetAt int64  `json:"descTime"`
	TopicSetBy string `json:"descOwner"`

	GroupCreated int64 `json:"creation"`

	Participants []struct {
		JID          string `json:"id"`
		IsAdmin      bool   `json:"isAdmin"`
		IsSuperAdmin bool   `json:"isSuperAdmin"`
	} `json:"participants"`
}

func (ext *ExtendedConn) GetGroupMetaData(jid string) (*GroupInfo, error) {
	data, err := ext.Conn.GetGroupMetaData(jid)
	if err != nil {
		return nil, fmt.Errorf("failed to get group metadata: %v", err)
	}
	content := <-data
	fmt.Println("GROUP METADATA", content)
	info := &GroupInfo{}
	err = json.Unmarshal([]byte(content), info)
	if err != nil {
		return info, fmt.Errorf("failed to unmarshal group metadata: %v", err)
	}

	for index, participant := range info.Participants {
		info.Participants[index].JID = strings.Replace(participant.JID, "@c.us", "@s.whatsapp.net", 1)
	}

	return info, nil
}

type ProfilePicInfo struct {
	URL string `json:"eurl"`
	Tag string `json:"tag"`
}

func (ppi *ProfilePicInfo) Download() (io.ReadCloser, error) {
	resp, err := http.Get(ppi.URL)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (ppi *ProfilePicInfo) DownloadBytes() ([]byte, error) {
	body, err := ppi.Download()
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := ioutil.ReadAll(body)
	return data, err
}

func (ext *ExtendedConn) GetProfilePicThumb(jid string) (*ProfilePicInfo, error) {
	data, err := ext.Conn.GetProfilePicThumb(jid)
	if err != nil {
		return nil, fmt.Errorf("failed to get avatar: %v", err)
	}
	content := <-data
	info := &ProfilePicInfo{}
	err = json.Unmarshal([]byte(content), info)
	if err != nil {
		return info, fmt.Errorf("failed to unmarshal avatar info: %v", err)
	}
	return info, nil
}
