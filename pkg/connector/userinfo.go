package connector

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exzerolog"
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
	wa.resyncQueueLock.Lock()
	jid := waid.ParseUserID(ghost.ID)
	if _, exists := wa.resyncQueue[jid]; !exists {
		wa.resyncQueue[jid] = resyncQueueItem{ghost: ghost}
		nextResyncIn := time.Until(wa.nextResync).String()
		if wa.nextResync.IsZero() {
			nextResyncIn = "never"
		}
		wa.UserLogin.Log.Debug().
			Stringer("jid", jid).
			Str("next_resync_in", nextResyncIn).
			Msg("Enqueued resync for ghost")
	}
	wa.resyncQueueLock.Unlock()
}

func (wa *WhatsAppClient) EnqueuePortalResync(portal *bridgev2.Portal) {
	jid, _ := waid.ParsePortalID(portal.ID)
	if jid.Server != types.GroupServer || portal.Metadata.(*waid.PortalMetadata).LastSync.Add(ResyncMinInterval).After(time.Now()) {
		return
	}
	wa.resyncQueueLock.Lock()
	if _, exists := wa.resyncQueue[jid]; !exists {
		wa.resyncQueue[jid] = resyncQueueItem{portal: portal}
		wa.UserLogin.Log.Debug().
			Stringer("jid", jid).
			Stringer("next_resync_in", time.Until(wa.nextResync)).
			Msg("Enqueued resync for portal")
	}
	wa.resyncQueueLock.Unlock()
}

func (wa *WhatsAppClient) ghostResyncLoop(ctx context.Context) {
	log := wa.UserLogin.Log.With().Str("action", "ghost resync loop").Logger()
	ctx = log.WithContext(ctx)
	wa.nextResync = time.Now().Add(ResyncLoopInterval).Add(-time.Duration(rand.IntN(ResyncJitterSeconds)) * time.Second)
	timer := time.NewTimer(time.Until(wa.nextResync))
	log.Info().Time("first_resync", wa.nextResync).Msg("Ghost resync queue starting")
	for {
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		queue := wa.rotateResyncQueue()
		timer.Reset(time.Until(wa.nextResync))
		if len(queue) > 0 {
			wa.doGhostResync(ctx, queue)
		} else {
			log.Trace().Msg("Nothing in background resync queue")
		}
	}
}

func (wa *WhatsAppClient) rotateResyncQueue() map[types.JID]resyncQueueItem {
	wa.resyncQueueLock.Lock()
	defer wa.resyncQueueLock.Unlock()
	wa.nextResync = time.Now().Add(ResyncLoopInterval)
	if len(wa.resyncQueue) == 0 {
		return nil
	}
	queue := wa.resyncQueue
	wa.resyncQueue = make(map[types.JID]resyncQueueItem)
	return queue
}

func (wa *WhatsAppClient) doGhostResync(ctx context.Context, queue map[types.JID]resyncQueueItem) {
	log := zerolog.Ctx(ctx)
	if !wa.IsLoggedIn() {
		log.Warn().Msg("Not logged in, skipping background resyncs")
		return
	}
	log.Debug().Msg("Starting background resyncs")
	defer log.Debug().Msg("Background resyncs finished")
	var ghostJIDs []types.JID
	var ghosts []*bridgev2.Ghost
	var portals []*bridgev2.Portal
	for jid, item := range queue {
		var lastSync time.Time
		if item.ghost != nil {
			lastSync = item.ghost.Metadata.(*waid.GhostMetadata).LastSync.Time
		} else if item.portal != nil {
			lastSync = item.portal.Metadata.(*waid.PortalMetadata).LastSync.Time
		}
		if lastSync.Add(ResyncMinInterval).After(time.Now()) {
			log.Debug().
				Stringer("jid", jid).
				Time("last_sync", lastSync).
				Msg("Not resyncing, last sync was too recent")
			continue
		}
		if item.ghost != nil {
			ghosts = append(ghosts, item.ghost)
			ghostJIDs = append(ghostJIDs, jid)
		} else if item.portal != nil {
			portals = append(portals, item.portal)
		}
	}
	for _, portal := range portals {
		wa.UserLogin.QueueRemoteEvent(&simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type: bridgev2.RemoteEventChatResync,
				LogContext: func(c zerolog.Context) zerolog.Context {
					return c.Str("sync_reason", "queue")
				},
				PortalKey: portal.PortalKey,
			},
			GetChatInfoFunc: wa.GetChatInfo,
		})
	}
	if len(ghostJIDs) == 0 {
		return
	}
	log.Debug().Array("jids", exzerolog.ArrayOfStringers(ghostJIDs)).Msg("Doing background sync for users")
	infos, err := wa.Client.GetUserInfo(ghostJIDs)
	if err != nil {
		log.Err(err).Msg("Failed to get user info for background sync")
		return
	}
	for _, ghost := range ghosts {
		jid := waid.ParseUserID(ghost.ID)
		info, ok := infos[jid]
		if !ok {
			log.Warn().Stringer("jid", jid).Msg("Didn't get info for puppet in background sync")
			continue
		}
		userInfo, err := wa.getUserInfo(ctx, jid, info.PictureID != "" && string(ghost.AvatarID) != info.PictureID)
		if err != nil {
			log.Err(err).Stringer("jid", jid).Msg("Failed to get user info for puppet in background sync")
			continue
		}
		ghost.UpdateInfo(ctx, userInfo)
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
		} else if !pnJID.IsEmpty() {
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
	forceSave := time.Since(meta.LastSync.Time) > 24*time.Hour
	meta.LastSync = jsontime.UnixNow()
	return forceSave
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
