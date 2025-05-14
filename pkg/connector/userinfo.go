package connector

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
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

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

const resyncMinInterval = 7 * 24 * time.Hour
const resyncLoopInterval = 4 * time.Hour

func (wa *WhatsAppClient) EnqueueGhostResync(ghost *bridgev2.Ghost) {
	if ghost.Metadata.(*waid.GhostMetadata).LastSync.Add(resyncMinInterval).After(time.Now()) {
		return
	}
	wa.resyncQueueLock.Lock()
	jid := waid.ParseUserID(ghost.ID)
	if _, exists := wa.resyncQueue[jid]; !exists {
		wa.resyncQueue[jid] = resyncQueueItem{ghost: ghost}
		wa.UserLogin.Log.Debug().
			Stringer("jid", jid).
			Stringer("next_resync_in", time.Until(wa.nextResync)).
			Msg("Enqueued resync for ghost")
	}
	wa.resyncQueueLock.Unlock()
}

func (wa *WhatsAppClient) EnqueuePortalResync(portal *bridgev2.Portal) {
	jid, _ := waid.ParsePortalID(portal.ID)
	if jid.Server != types.GroupServer || portal.Metadata.(*waid.PortalMetadata).LastSync.Add(resyncMinInterval).After(time.Now()) {
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
	wa.nextResync = time.Now().Add(resyncLoopInterval).Add(-time.Duration(rand.IntN(3600)) * time.Second)
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
	wa.nextResync = time.Now().Add(resyncLoopInterval)
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
		if lastSync.Add(resyncMinInterval).After(time.Now()) {
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
		wa.Main.Bridge.QueueRemoteEvent(wa.UserLogin, &simplevent.ChatResync{
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
	}
}

func (wa *WhatsAppClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	if ghost.Name != "" {
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
	} else if jid == types.PSAJID {
		contact.PushName = "WhatsApp"
	}
	var phone string
	if jid.Server == types.DefaultUserServer {
		phone = "+" + jid.User
	} else if jid.Server == types.HiddenUserServer {
		pnJID, err := wa.GetStore().LIDs.GetPNForLID(ctx, jid)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Stringer("lid", jid).Msg("Failed to get PN for LID")
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
		Identifiers:  []string{fmt.Sprintf("tel:+%s", jid.User)},
		ExtraUpdates: updateGhostLastSyncAt,
	}
	if jid.Server == types.BotServer {
		ui.Identifiers = []string{}
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

func (wa *WhatsAppClient) resyncContacts(forceAvatarSync bool) {
	log := wa.UserLogin.Log.With().Str("action", "resync contacts").Logger()
	ctx := log.WithContext(context.Background())
	contacts, err := wa.GetStore().Contacts.GetAllContacts(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to get cached contacts")
		return
	}
	log.Info().Int("contact_count", len(contacts)).Msg("Resyncing displaynames with contact info")
	for jid, contact := range contacts {
		ghost, err := wa.Main.Bridge.GetGhostByID(ctx, waid.MakeUserID(jid))
		if err != nil {
			log.Err(err).Msg("Failed to get ghost")
		} else if ghost != nil {
			ghost.UpdateInfo(ctx, wa.contactToUserInfo(ctx, jid, contact, forceAvatarSync || ghost.AvatarID == ""))
		}
	}
}
