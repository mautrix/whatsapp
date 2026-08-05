// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2026 Tulir Asokan
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

package connector

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"go.mau.fi/whatsmeow/types"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

func (wa *WhatsAppClient) FindAltTargetMessage(ctx context.Context, targetMsg networkid.MessageID, evt bridgev2.RemoteEventWithTargetMessage) (alts []networkid.MessageID, err error) {
	parsed, err := waid.ParseMessageID(targetMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to parse target message ID: %w", err)
	}
	altSender, err := wa.GetStore().GetAltJID(ctx, parsed.Sender)
	if err != nil {
		return nil, err
	}
	var altChat types.JID
	if parsed.Chat.Server == types.HiddenUserServer {
		altChat, err = wa.GetStore().LIDs.GetPNForLID(ctx, parsed.Chat)
		if err != nil {
			return nil, err
		}
	}
	if !altSender.IsEmpty() {
		altSenderID := *parsed
		altSenderID.Sender = altSender
		alts = append(alts, altSenderID.String())
	}
	if !altChat.IsEmpty() {
		altChatID := *parsed
		altChatID.Chat = altChat
		if altSender.Server == types.DefaultUserServer {
			altChatID.Sender = altSender
		}
		alts = append(alts, altChatID.String())
	}
	return
}

func (wa *WhatsAppClient) checkAllPhonesInMessage(ctx context.Context, info *types.MessageSource) (ok bool) {
	for _, jid := range []types.JID{info.Sender, info.SenderAlt, info.Chat, info.RecipientAlt, info.BroadcastListOwner} {
		if !wa.reIDPhoneDMToLIDIfNeeded(ctx, jid) {
			return false
		}
	}
	return true
}

func (wa *WhatsAppClient) reIDPhoneDMToLIDIfNeeded(ctx context.Context, pn types.JID) (ok bool) {
	if pn.Server != types.DefaultUserServer {
		return true
	}
	portalKey := wa.makeWAPortalKey(pn)
	if wa.Main.unmigratedDMs.Has(portalKey) {
		lid, err := wa.GetStore().LIDs.GetLIDForPN(ctx, pn)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Stringer("pn", pn).Msg("Failed to get LID for PN")
			return false
		} else if lid.IsEmpty() {
			zerolog.Ctx(ctx).Warn().Stringer("pn", pn).Msg("No found LID for phone number")
			return true
		}
		zerolog.Ctx(ctx).Info().
			Object("portal_key", portalKey).
			Stringer("pn", pn).
			Stringer("lid", lid).
			Msg("Received event for portal in unmigrated DMs list, trying migration")
		_, err = wa.Main.reIDPhoneDMToLID(ctx, pn, lid, wa.UserLogin.ID)
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to re-ID phone DM to LID")
			return false
		}
	}
	return true
}

func (wa *WhatsAppConnector) reIDPhoneDMToLID(ctx context.Context, pn, lid types.JID, receiver networkid.UserLoginID) (bridgev2.ReIDResult, error) {
	pnKey := networkid.PortalKey{
		ID:       waid.MakePortalID(pn),
		Receiver: receiver,
	}
	lidKey := networkid.PortalKey{
		ID:       waid.MakePortalID(lid),
		Receiver: receiver,
	}
	result, portal, err := wa.Bridge.ReIDPortal(ctx, pnKey, lidKey)
	if err != nil {
		return result, err
	}
	if result == bridgev2.ReIDResultSourceReIDd || result == bridgev2.ReIDResultTargetDeletedAndSourceReIDd {
		var pnGhost, lidGhost *bridgev2.Ghost
		pnGhost, err = wa.Bridge.GetGhostByID(ctx, waid.MakeUserID(pn))
		if err != nil {
			return result, fmt.Errorf("failed to get PN ghost: %w", err)
		}
		lidGhost, err = wa.Bridge.GetGhostByID(ctx, waid.MakeUserID(lid))
		if err != nil {
			return result, fmt.Errorf("failed to get LID ghost: %w", err)
		}
		_, err = pnGhost.Intent.SendState(ctx, portal.MXID, event.StateMember, pnGhost.Intent.GetMXID().String(), &event.Content{
			Parsed: &event.MemberEventContent{Membership: event.MembershipLeave, Reason: "Migrating to LIDs"},
			Raw:    map[string]any{"com.beeper.exclude_from_timeline": true},
		}, time.Time{})
		if err != nil {
			return result, fmt.Errorf("failed to send leave event for PN ghost: %w", err)
		}
		_, err = wa.Bridge.Bot.SendState(ctx, portal.MXID, event.StateMember, lidGhost.Intent.GetMXID().String(), &event.Content{
			Parsed: &event.MemberEventContent{Membership: event.MembershipInvite, Reason: "Migrating to LIDs"},
			Raw:    map[string]any{"com.beeper.exclude_from_timeline": true},
		}, time.Time{})
		if err != nil {
			return result, fmt.Errorf("failed to send invite event for LID ghost: %w", err)
		}
		_, err = lidGhost.Intent.SendState(ctx, portal.MXID, event.StateMember, lidGhost.Intent.GetMXID().String(), &event.Content{
			Parsed: &event.MemberEventContent{Membership: event.MembershipJoin, Reason: "Migrating to LIDs"},
			Raw:    map[string]any{"com.beeper.exclude_from_timeline": true},
		}, time.Time{})
		if err != nil {
			return result, fmt.Errorf("failed to send join event for LID ghost: %w", err)
		}
		portal.OtherUserID = lidGhost.ID
		err = portal.Save(ctx)
		if err != nil {
			return result, fmt.Errorf("failed to save portal after re-ID: %w", err)
		}
		portal.UpdateBridgeInfo(ctx)
	}
	return result, nil
}

var scanPortalKey = dbutil.ConvertRowFn[networkid.PortalKey](func(row dbutil.Scannable) (key networkid.PortalKey, err error) {
	err = row.Scan(&key.ID, &key.Receiver)
	return
})

func (wa *WhatsAppConnector) migrateToLIDDMs(ctx context.Context) error {
	if wa.Bridge.Background {
		if wa.Bridge.DB.KV.Get(ctx, "whatsapp_lid_dms_migrated") == "true" {
			return nil
		}
		return fmt.Errorf("can't migrate to LID DMs in background mode")
	}
	log := zerolog.Ctx(ctx).With().Str("action", "migrate to lid dms").Logger()
	const findPNPortals = "SELECT id, receiver FROM portal WHERE bridge_id=$1 AND room_type='dm' AND id LIKE '%@s.whatsapp.net'"
	pnPortalKeys, err := scanPortalKey.NewRowIter(wa.Bridge.DB.Query(ctx, findPNPortals, wa.Bridge.ID)).AsList()
	if err != nil {
		return fmt.Errorf("failed to get phone number portals: %w", err)
	}
	var updatedPortals, missingLID int
	for _, key := range pnPortalKeys {
		pnJID, err := waid.ParsePortalID(key.ID)
		if err != nil {
			log.Warn().Err(err).Str("portal_id", string(key.ID)).Msg("Failed to parse portal ID")
			continue
		} else if pnJID.Server != types.DefaultUserServer {
			continue
		}
		lid, err := wa.DeviceStore.LIDMap.GetLIDForPN(ctx, pnJID)
		if err != nil {
			return fmt.Errorf("failed to get LID for PN portal %s: %w", key.ID, err)
		} else if lid.IsEmpty() {
			log.Warn().Stringer("pn", pnJID).Msg("No LID for PN portal")
			wa.unmigratedDMs.Add(key)
			missingLID++
			continue
		}
		res, err := wa.reIDPhoneDMToLID(ctx, pnJID, lid, key.Receiver)
		if err != nil {
			return fmt.Errorf("failed to re-ID %s to %s: %w", pnJID, lid, err)
		}
		updatedPortals++
		log.Info().
			Stringer("pn", pnJID).
			Stringer("lid", lid).
			Stringer("result", res).
			Msg("Re-ID'd phone number DM portal")
	}
	log.Info().
		Int("updated_portals", updatedPortals).
		Int("total_pn_portals", len(pnPortalKeys)).
		Int("missing_lid", missingLID).
		Msg("Finished re-IDing phone number DM portals")
	wa.Bridge.DB.KV.Set(ctx, "whatsapp_lid_dms_migrated", "true")
	return nil
}
