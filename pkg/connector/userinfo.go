package connector

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/jsontime"
	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-whatsapp/pkg/connector/wadb"
	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

var ResyncMinInterval = 7 * 24 * time.Hour
var ResyncLoopInterval = 4 * time.Hour
var ResyncJitterSeconds = 3600

func (wa *WhatsAppClient) EnqueueGhostResync(ghost *bridgev2.Ghost) {
	if ghost.Metadata.(*waid.GhostMetadata).LastSync.Add(ResyncMinInterval).After(time.Now()) {
		return
	}
	jid := waid.ParseUserID(ghost.ID)
	wa.UserLogin.Log.Debug().Stringer("jid", jid).Msg("Enqueued resync for ghost")
	select {
	case wa.resyncQueueCh <- resyncQueueItem{ghost: ghost}:
	default:
		wa.UserLogin.Log.Warn().Stringer("jid", jid).Msg("Resync queue channel full, dropping ghost resync")
	}
}

func (wa *WhatsAppClient) EnqueuePortalResync(portal *bridgev2.Portal) {
	jid, _ := waid.ParsePortalID(portal.ID)
	meta := portal.Metadata.(*waid.PortalMetadata)
	_, capVer := wa.Main.GetBridgeInfoVersion()
	isOld := meta.LastSync.Add(ResyncMinInterval).Before(time.Now())
	versionMismatch := meta.BridgeCapsVersion != capVer
	if !isOld && !versionMismatch {
		return
	}
	wa.UserLogin.Log.Debug().Stringer("jid", jid).Msg("Enqueued resync for portal")
	select {
	case wa.resyncQueueCh <- resyncQueueItem{portal: portal}:
	default:
		wa.UserLogin.Log.Warn().Stringer("jid", jid).Msg("Resync queue channel full, dropping portal resync")
	}
}

func (wa *WhatsAppClient) resyncLoop(ctx context.Context) {
	log := wa.UserLogin.Log.With().Str("action", "resync loop").Logger()
	ctx = log.WithContext(ctx)
	log.Info().Msg("Resync queue starting")
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-wa.resyncQueueCh:
			// Re-check gating after dequeue
			if item.ghost != nil {
				lastSync := item.ghost.Metadata.(*waid.GhostMetadata).LastSync.Time
				if time.Since(lastSync) < ResyncMinInterval {
					continue
				}
			} else if item.portal != nil {
				meta := item.portal.Metadata.(*waid.PortalMetadata)
				_, capVer := wa.Main.GetBridgeInfoVersion()
				if time.Since(meta.LastSync.Time) < ResyncMinInterval && meta.BridgeCapsVersion == capVer {
					continue
				}
			}
			wa.doResync(ctx, item)
		}
	}
}

func (wa *WhatsAppClient) doResync(ctx context.Context, item resyncQueueItem) {
	log := zerolog.Ctx(ctx)
	if !wa.IsLoggedIn() {
		log.Warn().Msg("Not logged in, skipping background resync")
		return
	}
	log.Debug().Msg("Starting background resync")
	defer log.Debug().Msg("Background resync finished")
	if item.portal != nil {
		portal := item.portal
		wa.UserLogin.QueueRemoteEvent(&simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type:       bridgev2.RemoteEventChatResync,
				LogContext: func(c zerolog.Context) zerolog.Context { return c.Str("sync_reason", "resync_queue") },
				PortalKey:  portal.PortalKey,
			},
			GetChatInfoFunc: wa.GetChatInfo,
		})
		return
	}
	if item.ghost != nil {
		jid := waid.ParseUserID(item.ghost.ID)
		infos, err := wa.Client.GetUserInfo([]types.JID{jid})
		if err != nil {
			log.Err(err).Stringer("jid", jid).Msg("Failed to get user info for background sync")
			return
		}
		info, ok := infos[jid]
		if !ok {
			log.Warn().Stringer("jid", jid).Msg("Didn't get info for puppet in background sync")
			return
		}
		userInfo, err := wa.getUserInfo(ctx, jid, info.PictureID != "" && string(item.ghost.AvatarID) != info.PictureID)
		if err != nil {
			log.Err(err).Stringer("jid", jid).Msg("Failed to get user info for puppet in background sync")
			return
		}
		item.ghost.UpdateInfo(ctx, userInfo)
		wa.syncAltGhostWithInfo(ctx, jid, userInfo)
	}
}

func (wa *WhatsAppClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	if ghost.Name != "" && ghost.NameSet {
		wa.EnqueueGhostResync(ghost)
		return nil, nil
	}
	jid := waid.ParseUserID(ghost.ID)
	return wa.getUserInfo(ctx, jid, ghost.AvatarID == "")
}

func (wa *WhatsAppClient) getUserInfo(ctx context.Context, jid types.JID, fetchAvatar bool) (*bridgev2.UserInfo, error) {
	contact, err := wa.GetStore().Contacts.GetContact(ctx, jid)
	if err != nil {
		return nil, err
	}
	return wa.contactToUserInfo(ctx, jid, contact, fetchAvatar), nil
}

func (wa *WhatsAppClient) contactToUserInfo(ctx context.Context, jid types.JID, contact types.ContactInfo, getAvatar bool) *bridgev2.UserInfo {
	if jid == types.MetaAIJID && contact.PushName == jid.User {
		contact.PushName = "Meta AI"
	} else if jid == types.LegacyPSAJID || jid == types.PSAJID {
		contact.PushName = "WhatsApp"
	}
	var phone string
	if jid.Server == types.DefaultUserServer {
		phone = "+" + jid.User
	} else if jid.Server == types.HiddenUserServer {
		pnJID, err := wa.GetStore().LIDs.GetPNForLID(ctx, jid)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Stringer("lid", jid).Msg("Failed to get PN for LID")
		} else if pnJID.IsEmpty() {
			zerolog.Ctx(ctx).Debug().Stringer("lid", jid).Msg("Phone number not found for LID in contactToUserInfo")
		} else {
			phone = "+" + pnJID.User
			extraContact, err := wa.GetStore().Contacts.GetContact(ctx, pnJID)
			if err != nil {
				zerolog.Ctx(ctx).Err(err).
					Stringer("lid", jid).
					Stringer("pn_jid", pnJID).
					Msg("Failed to get contact info from PN")
			} else {
				if contact.FirstName == "" {
					contact.FirstName = extraContact.FirstName
				}
				if contact.FullName == "" {
					contact.FullName = extraContact.FullName
				}
				if contact.PushName == "" {
					contact.PushName = extraContact.PushName
				}
				if contact.BusinessName == "" {
					contact.BusinessName = extraContact.BusinessName
				}
			}
		}
	}
	ui := &bridgev2.UserInfo{
		Name:         ptr.Ptr(wa.Main.Config.FormatDisplayname(jid, phone, contact)),
		IsBot:        ptr.Ptr(jid.IsBot()),
		ExtraUpdates: updateGhostLastSyncAt,
	}
	if jid.Server == types.BotServer {
		ui.Identifiers = []string{}
	} else if phone != "" {
		ui.Identifiers = []string{fmt.Sprintf("tel:%s", phone)}
	}
	if getAvatar {
		ui.ExtraUpdates = bridgev2.MergeExtraUpdaters(ui.ExtraUpdates, wa.fetchGhostAvatar)
	}
	return ui
}

func updateGhostLastSyncAt(_ context.Context, ghost *bridgev2.Ghost) bool {
	meta := ghost.Metadata.(*waid.GhostMetadata)
	meta.LastSync = jsontime.UnixNow()
	return true
}

var expiryRegex = regexp.MustCompile("oe=([0-9A-Fa-f]+)")

func avatarInfoToCacheEntry(ctx context.Context, jid types.JID, avatar *types.ProfilePictureInfo) *wadb.AvatarCacheEntry {
	expiry := time.Now().Add(24 * time.Hour)
	match := expiryRegex.FindStringSubmatch(avatar.DirectPath)
	if len(match) == 2 {
		expiryUnix, err := strconv.ParseInt(match[1], 16, 64)
		if err == nil {
			expiry = time.Unix(expiryUnix, 0)
		} else {
			zerolog.Ctx(ctx).Warn().Err(err).
				Strs("match", match).
				Msg("Failed to parse expiry from avatar direct path")
		}
	}
	return &wadb.AvatarCacheEntry{
		EntityJID:  jid,
		AvatarID:   avatar.ID,
		DirectPath: avatar.DirectPath,
		Expiry:     jsontime.U(expiry),
		Gone:       false,
	}
}

func (wa *WhatsAppClient) makeDirectMediaAvatar(ctx context.Context, jid types.JID, avatar *types.ProfilePictureInfo, community bool) (*bridgev2.Avatar, error) {
	mxc, err := wa.Main.Bridge.Matrix.GenerateContentURI(ctx, waid.MakeAvatarMediaID(jid, avatar.ID, wa.UserLogin.ID, community))
	if err != nil {
		return nil, fmt.Errorf("failed to generate MXC URI: %w", err)
	}
	cacheEntry := avatarInfoToCacheEntry(ctx, jid, avatar)
	err = wa.Main.DB.AvatarCache.Put(ctx, cacheEntry)
	if err != nil {
		return nil, fmt.Errorf("failed to cache avatar info: %w", err)
	}
	hash := sha256.Sum256([]byte(avatar.ID))
	if len(avatar.Hash) == 32 {
		hash = [32]byte(avatar.Hash)
	}
	return &bridgev2.Avatar{
		ID:   networkid.AvatarID(avatar.ID),
		MXC:  mxc,
		Hash: hash,
	}, nil
}

func (wa *WhatsAppClient) fetchGhostAvatar(ctx context.Context, ghost *bridgev2.Ghost) bool {
	jid := waid.ParseUserID(ghost.ID)
	existingID := string(ghost.AvatarID)
	if existingID == "remove" || existingID == "unauthorized" {
		existingID = ""
	}
	var wrappedAvatar *bridgev2.Avatar
	avatar, err := wa.Client.GetProfilePictureInfo(jid, &whatsmeow.GetProfilePictureParams{ExistingID: existingID})
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
	} else if wa.Main.MsgConv.DirectMedia {
		wrappedAvatar, err = wa.makeDirectMediaAvatar(ctx, jid, avatar, false)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to prepare direct media avatar")
			return false
		}
	} else {
		wrappedAvatar = &bridgev2.Avatar{
			ID: networkid.AvatarID(avatar.ID),
			Get: func(ctx context.Context) ([]byte, error) {
				return wa.Client.DownloadMediaWithPath(ctx, avatar.DirectPath, nil, nil, nil, 0, "", "")
			},
		}
	}
	return ghost.UpdateAvatar(ctx, wrappedAvatar)
}

func (wa *WhatsAppClient) resyncContacts(forceAvatarSync, automatic bool) {
	log := wa.UserLogin.Log.With().Str("action", "resync contacts").Logger()
	ctx := log.WithContext(wa.Main.Bridge.BackgroundCtx)
	if automatic && wa.isNewLogin {
		log.Debug().Msg("Waiting for push name history sync before resyncing contacts")
		timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		_ = wa.pushNamesSynced.Wait(timeoutCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
	}
	contactStore := wa.GetStore().Contacts
	contacts, err := contactStore.GetAllContacts(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to get cached contacts")
		return
	}
	log.Info().Int("contact_count", len(contacts)).Msg("Resyncing displaynames with contact info")
	for jid := range contacts {
		if ctx.Err() != nil {
			return
		}
		ghost, err := wa.Main.Bridge.GetGhostByID(ctx, waid.MakeUserID(jid))
		if err != nil {
			log.Err(err).Stringer("jid", jid).Msg("Failed to get ghost")
			// Refetch contact info from the store to reduce the risk of races.
			// This should always hit the cache.
		} else if contact, err := contactStore.GetContact(ctx, jid); err != nil {
			log.Err(err).Stringer("jid", jid).Msg("Failed to get contact info")
		} else {
			userInfo := wa.contactToUserInfo(ctx, jid, contact, forceAvatarSync || ghost.AvatarID == "")
			ghost.UpdateInfo(ctx, userInfo)
			wa.syncAltGhostWithInfo(ctx, jid, userInfo)
		}
	}
}

func (wa *WhatsAppClient) syncAltGhostWithInfo(ctx context.Context, jid types.JID, info *bridgev2.UserInfo) {
	log := zerolog.Ctx(ctx)
	var altJID types.JID
	var err error
	if jid.Server == types.HiddenUserServer {
		altJID, err = wa.Device.LIDs.GetPNForLID(ctx, jid)
	} else if jid.Server == types.DefaultUserServer {
		altJID, err = wa.Device.LIDs.GetLIDForPN(ctx, jid)
	}
	if err != nil {
		log.Warn().Err(err).
			Stringer("jid", jid).
			Msg("Failed to get alternate JID for syncing user info")
		return
	} else if altJID.IsEmpty() {
		return
	}
	ghost, err := wa.Main.Bridge.GetGhostByID(ctx, waid.MakeUserID(altJID))
	if err != nil {
		log.Err(err).
			Stringer("alternate_jid", altJID).
			Stringer("jid", jid).
			Msg("Failed to get ghost for alternate JID")
		return
	}
	ghost.UpdateInfo(ctx, info)
	log.Debug().
		Stringer("jid", jid).
		Stringer("alternate_jid", altJID).
		Msg("Synced alternate ghost with info")
	go wa.syncRemoteProfile(ctx, ghost)
}
