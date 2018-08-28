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

package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"html"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"mime"
	"net/http"
	"strings"
	"sync"

	"github.com/Rhymen/go-whatsapp"
	waProto "github.com/Rhymen/go-whatsapp/binary/proto"
	"maunium.net/go/gomatrix"
	log "maunium.net/go/maulogger"
	"maunium.net/go/mautrix-appservice"
	"maunium.net/go/mautrix-whatsapp/database"
	"maunium.net/go/mautrix-whatsapp/types"
	"maunium.net/go/mautrix-whatsapp/whatsapp-ext"
)

func (user *User) GetPortalByMXID(mxid types.MatrixRoomID) *Portal {
	user.portalsLock.Lock()
	defer user.portalsLock.Unlock()
	portal, ok := user.portalsByMXID[mxid]
	if !ok {
		dbPortal := user.bridge.DB.Portal.GetByMXID(mxid)
		if dbPortal == nil || dbPortal.Owner != user.ID {
			return nil
		}
		portal = user.NewPortal(dbPortal)
		user.portalsByJID[portal.JID] = portal
		if len(portal.MXID) > 0 {
			user.portalsByMXID[portal.MXID] = portal
		}
	}
	return portal
}

func (user *User) GetPortalByJID(jid types.WhatsAppID) *Portal {
	user.portalsLock.Lock()
	defer user.portalsLock.Unlock()
	portal, ok := user.portalsByJID[jid]
	if !ok {
		dbPortal := user.bridge.DB.Portal.GetByJID(user.ID, jid)
		if dbPortal == nil {
			dbPortal = user.bridge.DB.Portal.New()
			dbPortal.JID = jid
			dbPortal.Owner = user.ID
			dbPortal.Insert()
		}
		portal = user.NewPortal(dbPortal)
		user.portalsByJID[portal.JID] = portal
		if len(portal.MXID) > 0 {
			user.portalsByMXID[portal.MXID] = portal
		}
	}
	return portal
}

func (user *User) GetAllPortals() []*Portal {
	user.portalsLock.Lock()
	defer user.portalsLock.Unlock()
	dbPortals := user.bridge.DB.Portal.GetAll(user.ID)
	output := make([]*Portal, len(dbPortals))
	for index, dbPortal := range dbPortals {
		portal, ok := user.portalsByJID[dbPortal.JID]
		if !ok {
			portal = user.NewPortal(dbPortal)
			user.portalsByJID[dbPortal.JID] = portal
			if len(dbPortal.MXID) > 0 {
				user.portalsByMXID[dbPortal.MXID] = portal
			}
		}
		output[index] = portal
	}
	return output
}

func (user *User) NewPortal(dbPortal *database.Portal) *Portal {
	return &Portal{
		Portal: dbPortal,
		user:   user,
		bridge: user.bridge,
		log:    user.log.Sub(fmt.Sprintf("Portal/%s", dbPortal.JID)),
	}
}

type Portal struct {
	*database.Portal

	user   *User
	bridge *Bridge
	log    log.Logger

	roomCreateLock sync.Mutex
}

func (portal *Portal) SyncParticipants(metadata *whatsappExt.GroupInfo) {
	changed := false
	levels, err := portal.MainIntent().PowerLevels(portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
		changed = true
	}
	for _, participant := range metadata.Participants {
		puppet := portal.user.GetPuppetByJID(participant.JID)
		puppet.Intent().EnsureJoined(portal.MXID)

		expectedLevel := 0
		if participant.IsSuperAdmin {
			expectedLevel = 95
		} else if participant.IsAdmin {
			expectedLevel = 50
		}
		changed = levels.EnsureUserLevel(puppet.MXID, expectedLevel) || changed

		if participant.JID == portal.user.JID() {
			changed = levels.EnsureUserLevel(portal.user.ID, expectedLevel) || changed
		}
	}
	if changed {
		portal.MainIntent().SetPowerLevels(portal.MXID, levels)
	}
}

func (portal *Portal) UpdateAvatar(avatar *whatsappExt.ProfilePicInfo) bool {
	if avatar == nil {
		var err error
		avatar, err = portal.user.Conn.GetProfilePicThumb(portal.JID)
		if err != nil {
			portal.log.Errorln(err)
			return false
		}
	}

	if portal.Avatar == avatar.Tag {
		return false
	}

	data, err := avatar.DownloadBytes()
	if err != nil {
		portal.log.Errorln("Failed to download avatar:", err)
		return false
	}

	mimeType := http.DetectContentType(data)
	resp, err := portal.MainIntent().UploadBytes(data, mimeType)
	if err != nil {
		portal.log.Errorln("Failed to upload avatar:", err)
		return false
	}

	_, err = portal.MainIntent().SetRoomAvatar(portal.MXID, resp.ContentURI)
	if err != nil {
		portal.log.Warnln("Failed to set room topic:", err)
		return false
	}
	portal.Avatar = avatar.Tag
	return true
}

func (portal *Portal) UpdateName(name string, setBy types.WhatsAppID) bool {
	if portal.Name != name {
		intent := portal.user.GetPuppetByJID(setBy).Intent()
		_, err := intent.SetRoomName(portal.MXID, name)
		if err == nil {
			portal.Name = name
			return true
		}
		portal.log.Warnln("Failed to set room name:", err)
	}
	return false
}

func (portal *Portal) UpdateTopic(topic string, setBy types.WhatsAppID) bool {
	if portal.Topic != topic {
		intent := portal.user.GetPuppetByJID(setBy).Intent()
		_, err := intent.SetRoomTopic(portal.MXID, topic)
		if err == nil {
			portal.Topic = topic
			return true
		}
		portal.log.Warnln("Failed to set room topic:", err)
	}
	return false
}

func (portal *Portal) UpdateMetadata() bool {
	metadata, err := portal.user.Conn.GetGroupMetaData(portal.JID)
	if err != nil {
		portal.log.Errorln(err)
		return false
	}
	portal.SyncParticipants(metadata)
	update := false
	update = portal.UpdateName(metadata.Name, metadata.NameSetBy) || update
	update = portal.UpdateTopic(metadata.Topic, metadata.TopicSetBy) || update
	return update
}

func (portal *Portal) Sync(contact whatsapp.Contact) {
	if len(portal.MXID) == 0 {
		if !portal.IsPrivateChat() {
			portal.Name = contact.Name
		}
		err := portal.CreateMatrixRoom()
		if err != nil {
			portal.log.Errorln("Failed to create portal room:", err)
			return
		}
	}

	if portal.IsPrivateChat() {
		return
	}

	update := false
	update = portal.UpdateMetadata() || update
	update = portal.UpdateAvatar(nil) || update
	if update {
		portal.Update()
	}
}

func (portal *Portal) GetBasePowerLevels() *gomatrix.PowerLevels {
	anyone := 0
	nope := 99
	return &gomatrix.PowerLevels{
		UsersDefault:    anyone,
		EventsDefault:   anyone,
		RedactPtr:       &anyone,
		StateDefaultPtr: &nope,
		BanPtr:          &nope,
		InvitePtr:       &nope,
		Users: map[string]int{
			portal.MainIntent().UserID: 100,
		},
		Events: map[gomatrix.EventType]int{
			gomatrix.StateRoomName:   anyone,
			gomatrix.StateRoomAvatar: anyone,
			gomatrix.StateTopic:      anyone,
		},
	}
}

func (portal *Portal) ChangeAdminStatus(jids []string, setAdmin bool) {
	levels, err := portal.MainIntent().PowerLevels(portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
	}
	newLevel := 0
	if setAdmin {
		newLevel = 50
	}
	changed := false
	for _, jid := range jids {
		puppet := portal.user.GetPuppetByJID(jid)
		changed = levels.EnsureUserLevel(puppet.MXID, newLevel) || changed

		if jid == portal.user.JID() {
			changed = levels.EnsureUserLevel(portal.user.ID, newLevel) || changed
		}
	}
	if changed {
		portal.MainIntent().SetPowerLevels(portal.MXID, levels)
	}
}

func (portal *Portal) RestrictMessageSending(restrict bool) {
	levels, err := portal.MainIntent().PowerLevels(portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
	}
	if restrict {
		levels.EventsDefault = 50
	} else {
		levels.EventsDefault = 0
	}
	portal.MainIntent().SetPowerLevels(portal.MXID, levels)
}

func (portal *Portal) RestrictMetadataChanges(restrict bool) {
	levels, err := portal.MainIntent().PowerLevels(portal.MXID)
	if err != nil {
		levels = portal.GetBasePowerLevels()
	}
	newLevel := 0
	if restrict {
		newLevel = 50
	}
	changed := false
	changed = levels.EnsureEventLevel(gomatrix.StateRoomName, true, newLevel) || changed
	changed = levels.EnsureEventLevel(gomatrix.StateRoomAvatar, true, newLevel) || changed
	changed = levels.EnsureEventLevel(gomatrix.StateTopic, true, newLevel) || changed
	if changed {
		portal.MainIntent().SetPowerLevels(portal.MXID, levels)
	}
}

func (portal *Portal) CreateMatrixRoom() error {
	portal.roomCreateLock.Lock()
	defer portal.roomCreateLock.Unlock()
	if len(portal.MXID) > 0 {
		return nil
	}

	name := portal.Name
	topic := portal.Topic
	isPrivateChat := false
	invite := []string{portal.user.ID}
	if portal.IsPrivateChat() {
		name = ""
		topic = "WhatsApp private chat"
		isPrivateChat = true
	}

	resp, err := portal.MainIntent().CreateRoom(&gomatrix.ReqCreateRoom{
		Visibility: "private",
		Name:       name,
		Topic:      topic,
		Invite:     invite,
		Preset:     "private_chat",
		IsDirect:   isPrivateChat,
		InitialState: []*gomatrix.Event{{
			Type: gomatrix.StatePowerLevels,
			Content: gomatrix.Content{
				PowerLevels: *portal.GetBasePowerLevels(),
			},
		}},
	})
	if err != nil {
		return err
	}
	portal.MXID = resp.RoomID
	portal.Update()
	return nil
}

func (portal *Portal) IsPrivateChat() bool {
	return strings.HasSuffix(portal.JID, whatsappExt.NewUserSuffix)
}

func (portal *Portal) MainIntent() *appservice.IntentAPI {
	if portal.IsPrivateChat() {
		return portal.user.GetPuppetByJID(portal.JID).Intent()
	}
	return portal.bridge.AppService.BotIntent()
}

func (portal *Portal) IsDuplicate(id types.WhatsAppMessageID) bool {
	msg := portal.bridge.DB.Message.GetByJID(portal.Owner, id)
	if msg != nil {
		return true
	}
	return false
}

func (portal *Portal) MarkHandled(jid types.WhatsAppMessageID, mxid types.MatrixEventID) {
	msg := portal.bridge.DB.Message.New()
	msg.Owner = portal.Owner
	msg.JID = jid
	msg.MXID = mxid
	msg.Insert()
}

func (portal *Portal) GetMessageIntent(info whatsapp.MessageInfo) *appservice.IntentAPI {
	if info.FromMe {
		if portal.IsPrivateChat() {
			// TODO handle own messages in private chats properly
			return nil
		}
		return portal.user.GetPuppetByJID(portal.user.JID()).Intent()
	} else if portal.IsPrivateChat() {
		return portal.MainIntent()
	} else if len(info.SenderJid) == 0 {
		if len(info.Source.GetParticipant()) != 0 {
			info.SenderJid = info.Source.GetParticipant()
		} else {
			return nil
		}
	}
	return portal.user.GetPuppetByJID(info.SenderJid).Intent()
}

func (portal *Portal) SetReply(content *gomatrix.Content, info whatsapp.MessageInfo) {
	if len(info.QuotedMessageID) == 0 {
		return
	}
	message := portal.bridge.DB.Message.GetByJID(portal.Owner, info.QuotedMessageID)
	if message != nil {
		event, err := portal.MainIntent().GetEvent(portal.MXID, message.MXID)
		if err != nil {
			portal.log.Warnln("Failed to get reply target:", err)
			return
		}
		content.SetReply(event)
	}
	return
}

func (portal *Portal) FormatWhatsAppMessage(content *gomatrix.Content) {
	output := html.EscapeString(content.Body)
	for regex, replacement := range portal.user.waReplString {
		output = regex.ReplaceAllString(output, replacement)
	}
	for regex, replacer := range portal.user.waReplFunc {
		output = regex.ReplaceAllStringFunc(output, replacer)
	}
	if output != content.Body {
		content.FormattedBody = output
		content.Format = gomatrix.FormatHTML
		for regex, replacer := range portal.user.waReplFuncText {
			content.Body = regex.ReplaceAllStringFunc(content.Body, replacer)
		}
	}
}

func (portal *Portal) HandleTextMessage(message whatsapp.TextMessage) {
	if portal.IsDuplicate(message.Info.Id) {
		return
	}

	err := portal.CreateMatrixRoom()
	if err != nil {
		portal.log.Errorln("Failed to create portal room:", err)
		return
	}

	intent := portal.GetMessageIntent(message.Info)
	if intent == nil {
		return
	}

	content := &gomatrix.Content{
		Body:    message.Text,
		MsgType: gomatrix.MsgText,
	}

	portal.FormatWhatsAppMessage(content)
	portal.SetReply(content, message.Info)

	intent.UserTyping(portal.MXID, false, 0)
	resp, err := intent.SendMassagedMessageEvent(portal.MXID, gomatrix.EventMessage, content, int64(message.Info.Timestamp*1000))
	if err != nil {
		portal.log.Errorfln("Failed to handle message %s: %v", message.Info.Id, err)
		return
	}
	portal.MarkHandled(message.Info.Id, resp.EventID)
	portal.log.Debugln("Handled message", message.Info.Id, "->", resp.EventID)
}

func (portal *Portal) HandleMediaMessage(download func() ([]byte, error), thumbnail []byte, info whatsapp.MessageInfo, mimeType, caption string) {
	if portal.IsDuplicate(info.Id) {
		return
	}

	err := portal.CreateMatrixRoom()
	if err != nil {
		portal.log.Errorln("Failed to create portal room:", err)
		return
	}

	intent := portal.GetMessageIntent(info)
	if intent == nil {
		return
	}

	data, err := download()
	if err != nil {
		portal.log.Errorln("Failed to download media:", err)
		return
	}

	uploaded, err := intent.UploadBytes(data, mimeType)
	if err != nil {
		portal.log.Errorln("Failed to upload media:", err)
		return
	}

	fileName := info.Id
	exts, _ := mime.ExtensionsByType(mimeType)
	if exts != nil && len(exts) > 0 {
		fileName += exts[0]
	}

	content := &gomatrix.Content{
		Body: fileName,
		URL:  uploaded.ContentURI,
		Info: &gomatrix.FileInfo{
			Size:     len(data),
			MimeType: mimeType,
		},
	}
	portal.SetReply(content, info)

	if thumbnail != nil {
		thumbnailMime := http.DetectContentType(thumbnail)
		uploadedThumbnail, _ := intent.UploadBytes(thumbnail, thumbnailMime)
		if uploadedThumbnail != nil {
			content.Info.ThumbnailURL = uploadedThumbnail.ContentURI
			cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
			content.Info.ThumbnailInfo = &gomatrix.FileInfo{
				Size:     len(thumbnail),
				Width:    cfg.Width,
				Height:   cfg.Height,
				MimeType: thumbnailMime,
			}
		}
	}

	switch strings.ToLower(strings.Split(mimeType, "/")[0]) {
	case "image":
		content.MsgType = gomatrix.MsgImage
		cfg, _, _ := image.DecodeConfig(bytes.NewReader(data))
		content.Info.Width = cfg.Width
		content.Info.Height = cfg.Height
	case "video":
		content.MsgType = gomatrix.MsgVideo
	case "audio":
		content.MsgType = gomatrix.MsgAudio
	default:
		content.MsgType = gomatrix.MsgFile
	}

	intent.UserTyping(portal.MXID, false, 0)
	ts := int64(info.Timestamp * 1000)
	resp, err := intent.SendMassagedMessageEvent(portal.MXID, gomatrix.EventMessage, content, ts)
	if err != nil {
		portal.log.Errorfln("Failed to handle message %s: %v", info.Id, err)
		return
	}

	if len(caption) > 0 {
		captionContent := &gomatrix.Content{
			Body:    caption,
			MsgType: gomatrix.MsgNotice,
		}

		portal.FormatWhatsAppMessage(captionContent)

		_, err := intent.SendMassagedMessageEvent(portal.MXID, gomatrix.EventMessage, captionContent, ts)
		if err != nil {
			portal.log.Warnfln("Failed to handle caption of message %s: %v", info.Id, err)
		}
		// TODO store caption mxid?
	}

	portal.MarkHandled(info.Id, resp.EventID)
	portal.log.Debugln("Handled message", info.Id, "->", resp.EventID)
}

func makeMessageID() *string {
	b := make([]byte, 10)
	rand.Read(b)
	str := strings.ToUpper(hex.EncodeToString(b))
	return &str
}

func (portal *Portal) downloadThumbnail(evt *gomatrix.Event) []byte {
	if evt.Content.Info == nil || len(evt.Content.Info.ThumbnailURL) == 0 {
		return nil
	}

	thumbnail, err := portal.MainIntent().DownloadBytes(evt.Content.Info.ThumbnailURL)
	if err != nil {
		portal.log.Errorln("Failed to download thumbnail in %s: %v", evt.ID, err)
		return nil
	}
	thumbnailType := http.DetectContentType(thumbnail)
	var img image.Image
	switch thumbnailType {
	case "image/png":
		img, err = png.Decode(bytes.NewReader(thumbnail))
	case "image/gif":
		img, err = gif.Decode(bytes.NewReader(thumbnail))
	case "image/jpeg":
		return thumbnail
	default:
		return nil
	}
	var buf bytes.Buffer
	err = jpeg.Encode(&buf, img, &jpeg.Options{
		Quality: jpeg.DefaultQuality,
	})
	if err != nil {
		portal.log.Errorln("Failed to re-encode thumbnail in %s: %v", evt.ID, err)
		return nil
	}
	return buf.Bytes()
}

func (portal *Portal) preprocessMatrixMedia(evt *gomatrix.Event, mediaType whatsapp.MediaType) *MediaUpload {
	if evt.Content.Info == nil {
		evt.Content.Info = &gomatrix.FileInfo{}
	}
	caption := evt.Content.Body
	exts, err := mime.ExtensionsByType(evt.Content.Info.MimeType)
	for _, ext := range exts {
		if strings.HasSuffix(caption, ext) {
			caption = ""
			break
		}
	}
	content, err := portal.MainIntent().DownloadBytes(evt.Content.URL)
	if err != nil {
		portal.log.Errorfln("Failed to download media in %s: %v", evt.ID, err)
		return nil
	}

	url, mediaKey, fileEncSHA256, fileSHA256, fileLength, err := portal.user.Conn.Upload(bytes.NewReader(content), mediaType)
	if err != nil {
		portal.log.Errorfln("Failed to upload media in %s: %v", evt.ID, err)
		return nil
	}

	return &MediaUpload{
		Caption:       caption,
		URL:           url,
		MediaKey:      mediaKey,
		FileEncSHA256: fileEncSHA256,
		FileSHA256:    fileSHA256,
		FileLength:    fileLength,
		Thumbnail:     portal.downloadThumbnail(evt),
	}
}

type MediaUpload struct {
	Caption       string
	URL           string
	MediaKey      []byte
	FileEncSHA256 []byte
	FileSHA256    []byte
	FileLength    uint64
	Thumbnail     []byte
}

func (portal *Portal) GetMessage(jid types.WhatsAppMessageID) *waProto.WebMessageInfo {
	node, err := portal.user.Conn.LoadMessagesBefore(portal.JID, jid, 1)
	if err != nil {
		return nil
	}
	msgs, ok := node.Content.([]interface{})
	if !ok {
		return nil
	}
	msg, ok := msgs[0].(*waProto.WebMessageInfo)
	if !ok {
		return nil
	}
	node, err = portal.user.Conn.LoadMessagesAfter(portal.JID, msg.GetKey().GetId(), 1)
	if err != nil {
		return nil
	}
	msgs, ok = node.Content.([]interface{})
	if !ok {
		return nil
	}
	msg, _ = msgs[0].(*waProto.WebMessageInfo)
	return msg
}

func (portal *Portal) HandleMatrixMessage(evt *gomatrix.Event) {
	ts := uint64(evt.Timestamp / 1000)
	status := waProto.WebMessageInfo_ERROR
	fromMe := true
	info := &waProto.WebMessageInfo{
		Key: &waProto.MessageKey{
			FromMe:    &fromMe,
			Id:        makeMessageID(),
			RemoteJid: &portal.JID,
		},
		MessageTimestamp: &ts,
		Message:          &waProto.Message{},
		Status:           &status,
	}
	ctxInfo := &waProto.ContextInfo{}
	replyToID := evt.Content.GetReplyTo()
	if len(replyToID) > 0 {
		evt.Content.RemoveReplyFallback()
		msg := portal.bridge.DB.Message.GetByMXID(replyToID)
		if msg != nil {
			origMsg := portal.GetMessage(msg.JID)
			if origMsg != nil {
				ctxInfo.StanzaId = &msg.JID
				replyMsgSender := origMsg.GetParticipant()
				if origMsg.GetKey().GetFromMe() {
					replyMsgSender = portal.user.JID()
				}
				ctxInfo.Participant = &replyMsgSender
				ctxInfo.QuotedMessage = []*waProto.Message{origMsg.Message}
			}
		}
	}
	var err error
	switch evt.Content.MsgType {
	case gomatrix.MsgText, gomatrix.MsgEmote:
		text := evt.Content.Body
		if evt.Content.Format == gomatrix.FormatHTML {
			text = portal.user.htmlParser.Parse(evt.Content.FormattedBody)
		}
		if evt.Content.MsgType == gomatrix.MsgEmote {
			text = "/me " + text
		}
		ctxInfo.MentionedJid = mentionRegex.FindAllString(text, -1)
		for index, mention := range ctxInfo.MentionedJid {
			ctxInfo.MentionedJid[index] = mention[1:] + whatsappExt.NewUserSuffix
		}
		if ctxInfo.StanzaId != nil || ctxInfo.MentionedJid != nil {
			info.Message.ExtendedTextMessage = &waProto.ExtendedTextMessage{
				Text:        &text,
				ContextInfo: ctxInfo,
			}
		} else {
			info.Message.Conversation = &text
		}
	case gomatrix.MsgImage:
		media := portal.preprocessMatrixMedia(evt, whatsapp.MediaImage)
		if media == nil {
			return
		}
		info.Message.ImageMessage = &waProto.ImageMessage{
			Caption:       &media.Caption,
			JpegThumbnail: media.Thumbnail,
			Url:           &media.URL,
			MediaKey:      media.MediaKey,
			Mimetype:      &evt.Content.GetInfo().MimeType,
			FileEncSha256: media.FileEncSHA256,
			FileSha256:    media.FileSHA256,
			FileLength:    &media.FileLength,
		}
	case gomatrix.MsgVideo:
		media := portal.preprocessMatrixMedia(evt, whatsapp.MediaVideo)
		if media == nil {
			return
		}
		duration := uint32(evt.Content.GetInfo().Duration)
		info.Message.VideoMessage = &waProto.VideoMessage{
			Caption:       &media.Caption,
			JpegThumbnail: media.Thumbnail,
			Url:           &media.URL,
			MediaKey:      media.MediaKey,
			Mimetype:      &evt.Content.GetInfo().MimeType,
			Seconds:       &duration,
			FileEncSha256: media.FileEncSHA256,
			FileSha256:    media.FileSHA256,
			FileLength:    &media.FileLength,
		}
	case gomatrix.MsgAudio:
		media := portal.preprocessMatrixMedia(evt, whatsapp.MediaAudio)
		if media == nil {
			return
		}
		duration := uint32(evt.Content.GetInfo().Duration)
		info.Message.AudioMessage = &waProto.AudioMessage{
			Url:           &media.URL,
			MediaKey:      media.MediaKey,
			Mimetype:      &evt.Content.GetInfo().MimeType,
			Seconds:       &duration,
			FileEncSha256: media.FileEncSHA256,
			FileSha256:    media.FileSHA256,
			FileLength:    &media.FileLength,
		}
	case gomatrix.MsgFile:
		media := portal.preprocessMatrixMedia(evt, whatsapp.MediaDocument)
		if media == nil {
			return
		}
		info.Message.DocumentMessage = &waProto.DocumentMessage{
			Url:           &media.URL,
			MediaKey:      media.MediaKey,
			Mimetype:      &evt.Content.GetInfo().MimeType,
			FileEncSha256: media.FileEncSHA256,
			FileSha256:    media.FileSHA256,
			FileLength:    &media.FileLength,
		}
	default:
		portal.log.Debugln("Unhandled Matrix event:", evt)
		return
	}
	err = portal.user.Conn.Send(info)
	portal.MarkHandled(info.GetKey().GetId(), evt.ID)
	if err != nil {
		portal.log.Errorfln("Error handling Matrix event %s: %v", evt.ID, err)
	} else {
		portal.log.Debugln("Handled Matrix event:", evt)
	}
}
