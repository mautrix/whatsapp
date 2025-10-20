package main

import (
	"net/http"
	"strings"

	"github.com/rs/zerolog/hlog"
	"go.mau.fi/util/exhttp"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-whatsapp/pkg/connector"
	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

type OtherUserInfo struct {
	MXID   id.UserID           `json:"mxid"`
	JID    types.JID           `json:"jid"`
	Name   string              `json:"displayname"`
	Avatar id.ContentURIString `json:"avatar_url"`
}

type PortalInfo struct {
	RoomID      id.RoomID        `json:"room_id"`
	OtherUser   *OtherUserInfo   `json:"other_user,omitempty"`
	GroupInfo   *types.GroupInfo `json:"group_info,omitempty"`
	JustCreated bool             `json:"just_created"`
}

type Error struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	ErrCode string `json:"errcode"`
}

func legacyProvContacts(w http.ResponseWriter, r *http.Request) {
	userLogin := m.Matrix.Provisioning.GetLoginForRequest(w, r)
	if userLogin == nil {
		return
	}
	if contacts, err := userLogin.Client.(*connector.WhatsAppClient).GetStore().Contacts.GetAllContacts(r.Context()); err != nil {
		hlog.FromRequest(r).Err(err).Msg("Failed to fetch all contacts")
		exhttp.WriteJSONResponse(w, http.StatusInternalServerError, Error{
			Error:   "Internal server error while fetching contact list",
			ErrCode: "failed to get contacts",
		})
	} else {
		augmentedContacts := map[types.JID]any{}
		for jid, contact := range contacts {
			var avatarURL id.ContentURIString
			if puppet, _ := m.Bridge.GetExistingGhostByID(r.Context(), waid.MakeUserID(jid)); puppet != nil {
				avatarURL = puppet.AvatarMXC
			}
			augmentedContacts[jid] = map[string]interface{}{
				"Found":        contact.Found,
				"FirstName":    contact.FirstName,
				"FullName":     contact.FullName,
				"PushName":     contact.PushName,
				"BusinessName": contact.BusinessName,
				"AvatarURL":    avatarURL,
			}
		}
		exhttp.WriteJSONResponse(w, http.StatusOK, augmentedContacts)
	}
}

func legacyProvResolveIdentifier(w http.ResponseWriter, r *http.Request) {
	number := r.PathValue("number")
	userLogin := m.Matrix.Provisioning.GetLoginForRequest(w, r)
	if userLogin == nil {
		return
	}
	startChat := strings.Contains(r.URL.Path, "/v1/pm/")
	resp, err := userLogin.Client.(*connector.WhatsAppClient).ResolveIdentifier(r.Context(), number, startChat)
	if err != nil {
		hlog.FromRequest(r).Warn().Err(err).Str("identifier", number).Msg("Failed to resolve identifier")
		matrix.RespondWithError(w, err, "Internal error resolving identifier")
		return
	}
	var portal *bridgev2.Portal
	if startChat {
		portal, err = m.Bridge.GetPortalByKey(r.Context(), resp.Chat.PortalKey)
		if err != nil {
			hlog.FromRequest(r).Warn().Err(err).Stringer("portal_key", resp.Chat.PortalKey).Msg("Failed to get portal by key")
			matrix.RespondWithError(w, err, "Internal error getting portal by key")
			return
		}
		err = portal.CreateMatrixRoom(r.Context(), userLogin, nil)
		if err != nil {
			hlog.FromRequest(r).Warn().Err(err).Stringer("portal_key", resp.Chat.PortalKey).Msg("Failed to create matrix room for portal")
			matrix.RespondWithError(w, err, "Internal error creating matrix room for portal")
			return
		}
	} else {
		portal, _ = m.Bridge.GetExistingPortalByKey(r.Context(), resp.Chat.PortalKey)
	}
	var roomID id.RoomID
	if portal != nil {
		roomID = portal.MXID
	}
	exhttp.WriteJSONResponse(w, http.StatusOK, PortalInfo{
		RoomID: roomID,
		OtherUser: &OtherUserInfo{
			JID:    waid.ParseUserID(resp.UserID),
			MXID:   resp.Ghost.Intent.GetMXID(),
			Name:   resp.Ghost.Name,
			Avatar: resp.Ghost.AvatarMXC,
		},
	})
}
