package connector

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"go.mau.fi/util/ptr"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"maunium.net/go/mautrix-whatsapp/pkg/waid"
)

func (wa *WhatsAppClient) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	jid := waid.ParseUserID(ghost.ID)
	contact, err := wa.Client.Store.Contacts.GetContact(jid)
	if err != nil {
		return nil, err
	}
	fetchAvatar := !ghost.Metadata.(*waid.GhostMetadata).AvatarFetchAttempted
	return wa.contactToUserInfo(ctx, jid, contact, fetchAvatar), nil
}

func (wa *WhatsAppClient) contactToUserInfo(ctx context.Context, jid types.JID, contact types.ContactInfo, getAvatar bool) *bridgev2.UserInfo {
	ui := &bridgev2.UserInfo{
		Name:        ptr.Ptr(wa.Main.Config.FormatDisplayname(jid, contact)),
		IsBot:       ptr.Ptr(jid.IsBot()),
		Identifiers: []string{fmt.Sprintf("tel:%s", jid.User)},
	}
	if getAvatar {
		ui.ExtraUpdates = bridgev2.MergeExtraUpdaters(ui.ExtraUpdates, markAvatarFetchAttempted)
		avatar, err := wa.Client.GetProfilePictureInfo(jid, &whatsmeow.GetProfilePictureParams{
			Preview:     false,
			IsCommunity: false,
		})
		if err != nil {
			zerolog.Ctx(ctx).Err(err).Msg("Failed to get avatar info")
		} else if avatar != nil {
			ui.Avatar = &bridgev2.Avatar{
				ID: networkid.AvatarID(avatar.ID),
				Get: func(ctx context.Context) ([]byte, error) {
					return wa.Client.DownloadMediaWithPath(avatar.DirectPath, nil, nil, nil, 0, "", "")
				},
			}
		}
	}
	return ui
}

func markAvatarFetchAttempted(_ context.Context, ghost *bridgev2.Ghost) bool {
	meta := ghost.Metadata.(*waid.GhostMetadata)
	if !meta.AvatarFetchAttempted {
		meta.AvatarFetchAttempted = true
		return true
	}
	return false
}
