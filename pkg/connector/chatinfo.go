package connector

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/jsontime"
	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/connector/wadb"
	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

func (wa *WhatsAppClient) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	portalJID, err := waid.ParsePortalID(portal.ID)
	if err != nil {
		return nil, err
	}
	return wa.getChatInfo(ctx, portalJID, nil)
}

func (wa *WhatsAppClient) getChatInfo(ctx context.Context, portalJID types.JID, conv *wadb.Conversation) (wrapped *bridgev2.ChatInfo, err error) {
	switch portalJID.Server {
	case types.DefaultUserServer, types.HiddenUserServer, types.BotServer:
		wrapped = wa.wrapDMInfo(portalJID)
	case types.BroadcastServer:
		if portalJID == types.StatusBroadcastJID {
			wrapped = wa.wrapStatusBroadcastInfo()
		} else {
			return nil, fmt.Errorf("broadcast list bridging is currently not supported")
		}
	case types.GroupServer:
		info, err := wa.Client.GetGroupInfo(portalJID)
		if err != nil {
			return nil, err
		}
		wrapped = wa.wrapGroupInfo(info)
		wrapped.ExtraUpdates = bridgev2.MergeExtraUpdaters(wrapped.ExtraUpdates, updatePortalLastSyncAt)
	case types.NewsletterServer:
		info, err := wa.Client.GetNewsletterInfo(portalJID)
		if err != nil {
			return nil, err
		}
		wrapped = wa.wrapNewsletterInfo(info)
	default:
		return nil, fmt.Errorf("unsupported server %s", portalJID.Server)
	}
	wa.addExtrasToWrapped(ctx, portalJID, wrapped, conv)
	return wrapped, nil
}

func (wa *WhatsAppClient) addExtrasToWrapped(ctx context.Context, portalJID types.JID, wrapped *bridgev2.ChatInfo, conv *wadb.Conversation) {
	if conv == nil {
		var err error
		conv, err = wa.Main.DB.Conversation.Get(ctx, wa.UserLogin.ID, portalJID)
		if err != nil {
			zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to get history sync conversation info")
		}
	}
	if conv != nil {
		wa.applyHistoryInfo(wrapped, conv)
	}
	wa.applyChatSettings(ctx, portalJID, wrapped)
}

func updatePortalLastSyncAt(_ context.Context, portal *bridgev2.Portal) bool {
	meta := portal.Metadata.(*waid.PortalMetadata)
	forceSave := time.Since(meta.LastSync.Time) > 24*time.Hour
	meta.LastSync = jsontime.UnixNow()
	return forceSave
}

func updateDisappearingTimerSetAt(ts int64) bridgev2.ExtraUpdater[*bridgev2.Portal] {
	return func(_ context.Context, portal *bridgev2.Portal) bool {
		meta := portal.Metadata.(*waid.PortalMetadata)
		if meta.DisappearingTimerSetAt != ts {
			meta.DisappearingTimerSetAt = ts
			return true
		}
		return false
	}
}

func (wa *WhatsAppClient) applyChatSettings(ctx context.Context, chatID types.JID, info *bridgev2.ChatInfo) {
	chat, err := wa.GetStore().ChatSettings.GetChatSettings(ctx, chatID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to get chat settings")
		return
	}
	info.UserLocal = &bridgev2.UserLocalPortalInfo{
		MutedUntil: ptr.Ptr(chat.MutedUntil),
	}
	if chat.Pinned {
		info.UserLocal.Tag = ptr.Ptr(wa.Main.Config.PinnedTag)
	} else if chat.Archived {
		info.UserLocal.Tag = ptr.Ptr(wa.Main.Config.ArchiveTag)
	}
}

func (wa *WhatsAppClient) applyHistoryInfo(info *bridgev2.ChatInfo, conv *wadb.Conversation) {
	if conv == nil {
		return
	}
	info.CanBackfill = true
	info.UserLocal = &bridgev2.UserLocalPortalInfo{
		MutedUntil: ptr.Ptr(conv.MuteEndTime),
	}
	if ptr.Val(conv.Pinned) {
		info.UserLocal.Tag = ptr.Ptr(wa.Main.Config.PinnedTag)
	} else if ptr.Val(conv.Archived) {
		info.UserLocal.Tag = ptr.Ptr(wa.Main.Config.ArchiveTag)
	}
	if info.Disappear == nil && ptr.Val(conv.EphemeralExpiration) > 0 {
		info.Disappear = &database.DisappearingSetting{
			Type:  database.DisappearingTypeAfterRead,
			Timer: time.Duration(*conv.EphemeralExpiration) * time.Second,
		}
		if conv.EphemeralSettingTimestamp != nil {
			info.ExtraUpdates = bridgev2.MergeExtraUpdaters(info.ExtraUpdates, updateDisappearingTimerSetAt(*conv.EphemeralSettingTimestamp))
		}
	}
}

const StatusBroadcastTopic = "WhatsApp status updates from your contacts"
const StatusBroadcastName = "WhatsApp Status Broadcast"
const BroadcastTopic = "WhatsApp broadcast list"
const UnnamedBroadcastName = "Unnamed broadcast list"
const PrivateChatTopic = "WhatsApp private chat"
const BotChatTopic = "WhatsApp chat with a bot"

func (wa *WhatsAppClient) wrapDMInfo(jid types.JID) *bridgev2.ChatInfo {
	info := &bridgev2.ChatInfo{
		Topic: ptr.Ptr(PrivateChatTopic),
		Members: &bridgev2.ChatMemberList{
			IsFull:           true,
			TotalMemberCount: 2,
			OtherUserID:      waid.MakeUserID(jid),
			MemberMap: map[networkid.UserID]bridgev2.ChatMember{
				waid.MakeUserID(jid):    {EventSender: wa.makeEventSender(jid)},
				waid.MakeUserID(wa.JID): {EventSender: wa.makeEventSender(wa.JID)},
			},
			PowerLevels: nil,
		},
		Type: ptr.Ptr(database.RoomTypeDM),
	}
	if jid.Server == types.BotServer {
		info.Topic = ptr.Ptr(BotChatTopic)
	}
	if jid == wa.JID.ToNonAD() {
		// For chats with self, force-split the members so the user's own ghost is always in the room.
		info.Members.MemberMap = map[networkid.UserID]bridgev2.ChatMember{
			waid.MakeUserID(jid): {EventSender: bridgev2.EventSender{Sender: waid.MakeUserID(jid)}},
			"":                   {EventSender: bridgev2.EventSender{IsFromMe: true}},
		}
	}
	return info
}

func (wa *WhatsAppClient) wrapStatusBroadcastInfo() *bridgev2.ChatInfo {
	userLocal := &bridgev2.UserLocalPortalInfo{}
	if wa.Main.Config.MuteStatusBroadcast {
		userLocal.MutedUntil = ptr.Ptr(event.MutedForever)
	}
	if wa.Main.Config.StatusBroadcastTag != "" {
		userLocal.Tag = ptr.Ptr(wa.Main.Config.StatusBroadcastTag)
	}
	return &bridgev2.ChatInfo{
		Name:  ptr.Ptr(StatusBroadcastName),
		Topic: ptr.Ptr(StatusBroadcastTopic),
		Members: &bridgev2.ChatMemberList{
			IsFull: false,
			MemberMap: map[networkid.UserID]bridgev2.ChatMember{
				waid.MakeUserID(wa.JID): {EventSender: wa.makeEventSender(wa.JID)},
			},
		},
		Type:        ptr.Ptr(database.RoomTypeDefault),
		UserLocal:   userLocal,
		CanBackfill: false,
	}
}

const (
	nobodyPL     = 99
	superAdminPL = 75
	adminPL      = 50
	defaultPL    = 0
)

func setDefaultSubGroupFlag(isCommunityAnnouncementGroup bool) bridgev2.ExtraUpdater[*bridgev2.Portal] {
	return func(_ context.Context, portal *bridgev2.Portal) bool {
		meta := portal.Metadata.(*waid.PortalMetadata)
		if meta.CommunityAnnouncementGroup != isCommunityAnnouncementGroup {
			meta.CommunityAnnouncementGroup = isCommunityAnnouncementGroup
			return true
		}
		return false
	}
}

func setAddressingMode(mode types.AddressingMode) bridgev2.ExtraUpdater[*bridgev2.Portal] {
	return func(_ context.Context, portal *bridgev2.Portal) bool {
		meta := portal.Metadata.(*waid.PortalMetadata)
		if meta.AddressingMode != mode {
			meta.AddressingMode = mode
			return true
		}
		return false
	}
}

func (wa *WhatsAppClient) wrapGroupInfo(info *types.GroupInfo) *bridgev2.ChatInfo {
	sendEventPL := defaultPL
	if info.IsAnnounce && !info.IsDefaultSubGroup {
		sendEventPL = adminPL
	}
	metaChangePL := defaultPL
	if info.IsLocked {
		metaChangePL = adminPL
	}
	extraUpdater := bridgev2.MergeExtraUpdaters(
		wa.makePortalAvatarFetcher("", types.EmptyJID, time.Time{}),
		setDefaultSubGroupFlag(info.IsDefaultSubGroup && info.IsAnnounce),
		setAddressingMode(info.AddressingMode),
	)
	wrapped := &bridgev2.ChatInfo{
		Name:  ptr.Ptr(info.Name),
		Topic: ptr.Ptr(info.Topic),
		Members: &bridgev2.ChatMemberList{
			IsFull:           !info.IsIncognito,
			TotalMemberCount: len(info.Participants),
			MemberMap:        make(map[networkid.UserID]bridgev2.ChatMember, len(info.Participants)),
			PowerLevels: &bridgev2.PowerLevelOverrides{
				EventsDefault: &sendEventPL,
				StateDefault:  ptr.Ptr(nobodyPL),
				Ban:           ptr.Ptr(nobodyPL),
				// TODO allow invites if bridge config says to allow them, or maybe if relay mode is enabled?
				Events: map[event.Type]int{
					event.StateRoomName:   metaChangePL,
					event.StateRoomAvatar: metaChangePL,
					event.StateTopic:      metaChangePL,
					event.EventReaction:   defaultPL,
					event.EventRedaction:  defaultPL,
					// TODO always allow poll responses
				},
			},
		},
		Disappear: &database.DisappearingSetting{
			Type:  database.DisappearingTypeAfterRead,
			Timer: time.Duration(info.DisappearingTimer) * time.Second,
		},
		ExtraUpdates: extraUpdater,
	}
	for _, pcp := range info.Participants {
		member := bridgev2.ChatMember{
			EventSender: wa.makeEventSender(pcp.JID),
			Membership:  event.MembershipJoin,
		}
		if pcp.IsSuperAdmin {
			member.PowerLevel = ptr.Ptr(superAdminPL)
		} else if pcp.IsAdmin {
			member.PowerLevel = ptr.Ptr(adminPL)
		} else {
			member.PowerLevel = ptr.Ptr(defaultPL)
		}
		wrapped.Members.MemberMap[waid.MakeUserID(pcp.JID)] = member
	}

	if !info.LinkedParentJID.IsEmpty() {
		wrapped.ParentID = ptr.Ptr(waid.MakePortalID(info.LinkedParentJID))
	}
	if info.IsParent {
		wrapped.Type = ptr.Ptr(database.RoomTypeSpace)
	} else {
		wrapped.Type = ptr.Ptr(database.RoomTypeDefault)
	}
	return wrapped
}

func (wa *WhatsAppClient) wrapGroupInfoChange(evt *events.GroupInfo) *bridgev2.ChatInfoChange {
	var changes *bridgev2.ChatInfo
	if evt.Name != nil || evt.Topic != nil || evt.Ephemeral != nil || evt.Unlink != nil || evt.Link != nil {
		changes = &bridgev2.ChatInfo{}
		if evt.Name != nil {
			changes.Name = &evt.Name.Name
		}
		if evt.Topic != nil {
			changes.Topic = &evt.Topic.Topic
		}
		if evt.Ephemeral != nil {
			changes.Disappear = &database.DisappearingSetting{
				Type:  database.DisappearingTypeAfterRead,
				Timer: time.Duration(evt.Ephemeral.DisappearingTimer) * time.Second,
			}
			if !evt.Ephemeral.IsEphemeral {
				changes.Disappear = &database.DisappearingSetting{}
			}
		}
		if evt.Unlink != nil {
			// TODO ensure this doesn't race with link to a new group?
			changes.ParentID = ptr.Ptr(networkid.PortalID(""))
		}
		if evt.Link != nil && evt.Link.Type == types.GroupLinkChangeTypeParent {
			changes.ParentID = ptr.Ptr(waid.MakePortalID(evt.Link.Group.JID))
		}
	}
	var memberChanges *bridgev2.ChatMemberList
	if evt.Join != nil || evt.Leave != nil || evt.Promote != nil || evt.Demote != nil {
		memberChanges = &bridgev2.ChatMemberList{
			MemberMap: make(map[networkid.UserID]bridgev2.ChatMember),
		}
		for _, userID := range evt.Join {
			memberChanges.MemberMap[waid.MakeUserID(userID)] = bridgev2.ChatMember{
				EventSender: wa.makeEventSender(userID),
			}
		}
		for _, userID := range evt.Promote {
			memberChanges.MemberMap[waid.MakeUserID(userID)] = bridgev2.ChatMember{
				EventSender: wa.makeEventSender(userID),
				PowerLevel:  ptr.Ptr(adminPL),
			}
		}
		for _, userID := range evt.Demote {
			memberChanges.MemberMap[waid.MakeUserID(userID)] = bridgev2.ChatMember{
				EventSender: wa.makeEventSender(userID),
				PowerLevel:  ptr.Ptr(defaultPL),
			}
		}
		for _, userID := range evt.Leave {
			memberChanges.MemberMap[waid.MakeUserID(userID)] = bridgev2.ChatMember{
				EventSender: wa.makeEventSender(userID),
				Membership:  event.MembershipLeave,
			}
		}
	}
	if evt.Announce != nil || evt.Locked != nil {
		if memberChanges == nil {
			memberChanges = &bridgev2.ChatMemberList{}
		}
		memberChanges.PowerLevels = &bridgev2.PowerLevelOverrides{}
		if evt.Announce != nil {
			if evt.Announce.IsAnnounce {
				memberChanges.PowerLevels.EventsDefault = ptr.Ptr(adminPL)
			} else {
				memberChanges.PowerLevels.EventsDefault = ptr.Ptr(defaultPL)
			}
		}
		if evt.Locked != nil {
			metaChangePL := defaultPL
			if evt.Locked.IsLocked {
				metaChangePL = adminPL
			}
			memberChanges.PowerLevels.Events = map[event.Type]int{
				event.StateRoomName:   metaChangePL,
				event.StateRoomAvatar: metaChangePL,
				event.StateTopic:      metaChangePL,
			}
		}
	}
	return &bridgev2.ChatInfoChange{
		ChatInfo:      changes,
		MemberChanges: memberChanges,
	}
}

func (wa *WhatsAppClient) makePortalAvatarFetcher(avatarID string, sender types.JID, ts time.Time) func(context.Context, *bridgev2.Portal) bool {
	return func(ctx context.Context, portal *bridgev2.Portal) bool {
		jid, _ := waid.ParsePortalID(portal.ID)
		existingID := string(portal.AvatarID)
		if avatarID != "" && avatarID == existingID {
			return false
		}
		if existingID == "remove" || existingID == "unauthorized" {
			existingID = ""
		}
		var wrappedAvatar *bridgev2.Avatar
		avatar, err := wa.Client.GetProfilePictureInfo(jid, &whatsmeow.GetProfilePictureParams{
			ExistingID:  existingID,
			IsCommunity: portal.RoomType == database.RoomTypeSpace,
		})
		if errors.Is(err, whatsmeow.ErrProfilePictureNotSet) {
			wrappedAvatar = &bridgev2.Avatar{
				ID:     "remove",
				Remove: true,
			}
		} else if errors.Is(err, whatsmeow.ErrProfilePictureUnauthorized) {
			wrappedAvatar = &bridgev2.Avatar{
				ID:     "unauthorized",
				Remove: true,
			}
		} else if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to get avatar info")
			return false
		} else if avatar == nil {
			return false
		} else {
			wrappedAvatar = &bridgev2.Avatar{
				ID: networkid.AvatarID(avatar.ID),
				Get: func(ctx context.Context) ([]byte, error) {
					return wa.Client.DownloadMediaWithPath(ctx, avatar.DirectPath, nil, nil, nil, 0, "", "")
				},
			}
		}
		var evtSender bridgev2.EventSender
		if !sender.IsEmpty() {
			evtSender = wa.makeEventSender(sender)
		}
		senderIntent := portal.GetIntentFor(ctx, evtSender, wa.UserLogin, bridgev2.RemoteEventChatInfoChange)
		//lint:ignore SA1019 TODO invent a cleaner way to fetch avatar metadata before updating?
		return portal.Internal().UpdateAvatar(ctx, wrappedAvatar, senderIntent, ts)
	}
}

func (wa *WhatsAppClient) wrapNewsletterInfo(info *types.NewsletterMetadata) *bridgev2.ChatInfo {
	ownPowerLevel := defaultPL
	var mutedUntil *time.Time
	if info.ViewerMeta != nil {
		switch info.ViewerMeta.Role {
		case types.NewsletterRoleAdmin:
			ownPowerLevel = adminPL
		case types.NewsletterRoleOwner:
			ownPowerLevel = superAdminPL
		}
		switch info.ViewerMeta.Mute {
		case types.NewsletterMuteOn:
			mutedUntil = &event.MutedForever
		case types.NewsletterMuteOff:
			mutedUntil = &bridgev2.Unmuted
		}
	}
	avatar := &bridgev2.Avatar{}
	if info.ThreadMeta.Picture != nil {
		avatar.ID = networkid.AvatarID(info.ThreadMeta.Picture.ID)
		avatar.Get = func(ctx context.Context) ([]byte, error) {
			return wa.Client.DownloadMediaWithPath(ctx, info.ThreadMeta.Picture.DirectPath, nil, nil, nil, 0, "", "")
		}
	} else if info.ThreadMeta.Preview.ID != "" {
		avatar.ID = networkid.AvatarID(info.ThreadMeta.Preview.ID)
		avatar.Get = func(ctx context.Context) ([]byte, error) {
			meta, err := wa.Client.GetNewsletterInfo(info.ID)
			if err != nil {
				return nil, fmt.Errorf("failed to fetch full res avatar info: %w", err)
			} else if meta.ThreadMeta.Picture == nil {
				return nil, fmt.Errorf("full res avatar info is missing")
			}
			return wa.Client.DownloadMediaWithPath(ctx, meta.ThreadMeta.Picture.DirectPath, nil, nil, nil, 0, "", "")
		}
	} else {
		avatar.ID = "remove"
		avatar.Remove = true
	}
	return &bridgev2.ChatInfo{
		Name:   ptr.Ptr(info.ThreadMeta.Name.Text),
		Topic:  ptr.Ptr(info.ThreadMeta.Description.Text),
		Avatar: avatar,
		UserLocal: &bridgev2.UserLocalPortalInfo{
			MutedUntil: mutedUntil,
		},
		Members: &bridgev2.ChatMemberList{
			TotalMemberCount: info.ThreadMeta.SubscriberCount,
			MemberMap: map[networkid.UserID]bridgev2.ChatMember{
				waid.MakeUserID(wa.JID): {
					EventSender: wa.makeEventSender(wa.JID),
					PowerLevel:  &ownPowerLevel,
				},
			},
			PowerLevels: &bridgev2.PowerLevelOverrides{
				EventsDefault: ptr.Ptr(adminPL),
				StateDefault:  ptr.Ptr(nobodyPL),
				Ban:           ptr.Ptr(nobodyPL),
				Events: map[event.Type]int{
					event.StateRoomName:   adminPL,
					event.StateRoomAvatar: adminPL,
					event.StateTopic:      adminPL,
					event.EventReaction:   defaultPL,
					event.EventRedaction:  defaultPL,
					// TODO always allow poll responses
				},
			},
		},
		Type: ptr.Ptr(database.RoomTypeDefault),
	}
}
