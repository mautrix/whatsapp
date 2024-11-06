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

package waid

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

func MakeMediaID(messageInfo *types.MessageInfo, receiver networkid.UserLoginID) networkid.MediaID {
	compactChat := compactJID(messageInfo.Chat.ToNonAD())
	compactSender := compactJID(messageInfo.Sender.ToNonAD())
	receiverID := compactJID(ParseUserLoginID(receiver, 0))
	compactID := compactMsgID(messageInfo.ID)
	mediaID := make([]byte, 0, 3+len(compactChat)+len(compactSender)+len(compactID))
	mediaID = append(mediaID, byte(len(compactChat)))
	mediaID = append(mediaID, compactChat...)
	mediaID = append(mediaID, byte(len(compactSender)))
	mediaID = append(mediaID, compactSender...)
	mediaID = append(mediaID, byte(len(receiverID)))
	mediaID = append(mediaID, receiverID...)
	mediaID = append(mediaID, byte(len(compactID)))
	mediaID = append(mediaID, compactID...)
	return mediaID
}

func ParseMediaID(mediaID networkid.MediaID) (*ParsedMessageID, networkid.UserLoginID, error) {
	reader := bytes.NewReader(mediaID)
	chatJID, err := readCompact(reader, parseCompactJID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse chat JID: %w", err)
	}
	senderJID, err := readCompact(reader, parseCompactJID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse sender JID: %w", err)
	}
	receiverID, err := readCompact(reader, parseCompactJID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse receiver JID: %w", err)
	}
	id, err := readCompact(reader, parseCompactMsgID)
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse message ID: %w", err)
	}
	return &ParsedMessageID{
		Chat:   chatJID,
		Sender: senderJID,
		ID:     id,
	}, MakeUserLoginID(receiverID), nil
}

func isUpperHex(str string) bool {
	for _, c := range str {
		if (c < '0' || c > '9') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

const (
	compactJIDRawString   = 0
	compactJIDUserInt     = 1
	compactJIDUserDevice  = 2
	compactJIDGroupString = 3

	compactMsgIDRawString = 0
	compactMsgIDUpperHex  = 1
)

func compactMsgID(id types.MessageID) []byte {
	if isUpperHex(id) && len(id)%2 == 0 {
		msgID, err := hex.AppendDecode([]byte{compactMsgIDUpperHex}, []byte(id))
		if err == nil {
			return msgID
		}
	}
	return append([]byte{compactMsgIDRawString}, []byte(id)...)
}

func parseCompactMsgID(id []byte) (types.MessageID, error) {
	if len(id) == 0 {
		return "", fmt.Errorf("empty message ID")
	}
	switch id[0] {
	case compactMsgIDRawString:
		return types.MessageID(id[1:]), nil
	case compactMsgIDUpperHex:
		return types.MessageID(strings.ToUpper(hex.EncodeToString(id[1:]))), nil
	default:
		return "", fmt.Errorf("invalid compact message ID type")
	}
}

func compactJID(jid types.JID) []byte {
	if jid.Server == types.DefaultUserServer {
		userInt, err := strconv.ParseUint(jid.User, 10, 64)
		if err == nil {
			if jid.Device == 0 {
				return binary.BigEndian.AppendUint64([]byte{compactJIDUserInt}, userInt)
			} else {
				data := make([]byte, 1, 1+8+2)
				data[1] = compactJIDUserDevice
				data = binary.BigEndian.AppendUint64(data, userInt)
				data = binary.BigEndian.AppendUint16(data, jid.Device)
				return data
			}
		}
	} else if jid.Server == types.GroupServer {
		return append([]byte{compactJIDGroupString}, jid.User...)
	}
	return append([]byte{compactJIDRawString}, []byte(jid.String())...)
}

func parseCompactJID(jid []byte) (types.JID, error) {
	if len(jid) == 0 {
		return types.EmptyJID, fmt.Errorf("empty JID")
	}
	switch jid[0] {
	case compactJIDRawString:
		parsed, err := types.ParseJID(string(jid[1:]))
		return parsed, err
	case compactJIDUserInt:
		if len(jid) != 9 {
			return types.EmptyJID, fmt.Errorf("invalid compact user JID length")
		}
		return types.JID{
			User:   strconv.FormatUint(binary.BigEndian.Uint64(jid[1:]), 10),
			Server: types.DefaultUserServer,
		}, nil
	case compactJIDUserDevice:
		if len(jid) != 11 {
			return types.EmptyJID, fmt.Errorf("invalid compact user device JID length")
		}
		return types.JID{
			User:   strconv.FormatUint(binary.BigEndian.Uint64(jid[1:]), 10),
			Device: binary.BigEndian.Uint16(jid[9:]),
			Server: types.DefaultUserServer,
		}, nil
	case compactJIDGroupString:
		return types.JID{
			User:   string(jid[1:]),
			Server: types.GroupServer,
		}, nil
	default:
		return types.EmptyJID, fmt.Errorf("invalid compact JID type")
	}
}

func readCompact[T any](reader *bytes.Reader, fn func(data []byte) (T, error)) (T, error) {
	var defVal T
	length, err := reader.ReadByte()
	if err != nil {
		return defVal, err
	}
	data := make([]byte, length)
	_, err = io.ReadFull(reader, data)
	if err != nil {
		return defVal, err
	}
	return fn(data)
}
