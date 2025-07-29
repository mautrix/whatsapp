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
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"

	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

const (
	// Media ID types start from 255, because old media IDs didn't have a type byte and had the length at the start.
	mediaIDTypeMessage         = 255
	mediaIDTypeAvatar          = 254
	mediaIDTypeCommunityAvatar = 253
)

func MakeMediaID(messageInfo *types.MessageInfo, idOverride types.MessageID, receiver networkid.UserLoginID) networkid.MediaID {
	compactChat := compactJID(messageInfo.Chat.ToNonAD())
	compactSender := compactJID(messageInfo.Sender.ToNonAD())
	receiverID := compactJID(ParseUserLoginID(receiver, 0))
	var compactID []byte
	if idOverride != "" {
		compactID = compactMsgID(idOverride)
	} else {
		compactID = compactMsgID(messageInfo.ID)
	}
	mediaID := make([]byte, 0, 5+len(compactChat)+len(compactSender)+len(receiverID)+len(compactID))
	mediaID = append(mediaID, mediaIDTypeMessage)
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

func MakeAvatarMediaID(targetJID types.JID, id string, receiver networkid.UserLoginID, community bool) networkid.MediaID {
	compactTarget := compactJID(targetJID.ToNonAD())
	receiverID := compactJID(ParseUserLoginID(receiver, 0))
	mediaID := make([]byte, 0, 4+len(compactTarget)+len(id)+len(receiverID))
	if community {
		mediaID = append(mediaID, mediaIDTypeCommunityAvatar)
	} else {
		mediaID = append(mediaID, mediaIDTypeAvatar)
	}
	mediaID = append(mediaID, byte(len(compactTarget)))
	mediaID = append(mediaID, compactTarget...)
	mediaID = append(mediaID, byte(len(id)))
	mediaID = append(mediaID, id...)
	mediaID = append(mediaID, byte(len(receiverID)))
	mediaID = append(mediaID, receiverID...)
	return mediaID
}

type AvatarMediaInfo struct {
	TargetJID types.JID
	AvatarID  string
	Community bool
}

type ParsedMediaID struct {
	Message   *ParsedMessageID
	Avatar    *AvatarMediaInfo
	UserLogin networkid.UserLoginID
}

func ParseMediaID(mediaID networkid.MediaID) (*ParsedMediaID, error) {
	mediaIDType := mediaIDTypeMessage
	if mediaID[0] > 127 {
		mediaIDType = int(mediaID[0])
		mediaID = mediaID[1:]
	}
	var parsed ParsedMediaID
	switch mediaIDType {
	case mediaIDTypeMessage:
		chatJID, err := readCompact(&mediaID, parseCompactJID)
		if err != nil {
			return nil, fmt.Errorf("failed to parse chat JID: %w", err)
		}
		senderJID, err := readCompact(&mediaID, parseCompactJID)
		if err != nil {
			return nil, fmt.Errorf("failed to parse sender JID: %w", err)
		}
		receiverID, err := readCompact(&mediaID, parseCompactJID)
		if err != nil {
			return nil, fmt.Errorf("failed to parse receiver JID: %w", err)
		}
		id, err := readCompact(&mediaID, parseCompactMsgID)
		if err != nil {
			return nil, fmt.Errorf("failed to parse message ID: %w", err)
		}
		parsed.Message = &ParsedMessageID{
			Chat:   chatJID,
			Sender: senderJID,
			ID:     id,
		}
		parsed.UserLogin = MakeUserLoginID(receiverID)
	case mediaIDTypeAvatar, mediaIDTypeCommunityAvatar:
		targetJID, err := readCompact(&mediaID, parseCompactJID)
		if err != nil {
			return nil, fmt.Errorf("failed to parse target JID: %w", err)
		}
		avatarID, err := readCompact(&mediaID, parseString)
		if err != nil {
			return nil, fmt.Errorf("failed to parse avatar ID: %w", err)
		}
		receiverID, err := readCompact(&mediaID, parseCompactJID)
		if err != nil {
			return nil, fmt.Errorf("failed to parse receiver JID: %w", err)
		}
		parsed.Avatar = &AvatarMediaInfo{
			TargetJID: targetJID,
			AvatarID:  avatarID,
			Community: mediaIDType == mediaIDTypeCommunityAvatar,
		}
		parsed.UserLogin = MakeUserLoginID(receiverID)
	default:
		return nil, fmt.Errorf("unknown media ID type %d", mediaIDType)
	}
	return &parsed, nil
}

func parseString(data []byte) (string, error) {
	return string(data), nil
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

func readCompact[T any](data *networkid.MediaID, fn func(data []byte) (T, error)) (T, error) {
	var defVal T
	if len(*data) < 1 {
		return defVal, fmt.Errorf("%w (data too short to read length)", io.ErrUnexpectedEOF)
	}
	length := int((*data)[0])
	if len(*data) < length+1 {
		return defVal, fmt.Errorf("%w (wanted %d+1 bytes, only have %d)", io.ErrUnexpectedEOF, length, len(*data))
	}
	dataToParse := (*data)[1 : length+1]
	*data = (*data)[length+1:]
	return fn(dataToParse)
}
