package connector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	mxbridge "maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-whatsapp/pkg/connector/voip"
	"go.mau.fi/mautrix-whatsapp/pkg/connector/wadb"
	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

const (
	matrixRTCRingLifetime       = 90 * time.Second
	matrixRTCMembershipLifetime = 4 * time.Hour
	matrixRTCStickyDuration     = time.Hour
)

func (wa *WhatsAppClient) handleIncomingVOIPCall(call *meowcaller.Call) {
	if call == nil {
		return
	}
	ctx := wa.UserLogin.Log.WithContext(withoutCancelOrBackground(wa.Main.Bridge.BackgroundCtx))
	err := wa.announceIncomingMatrixRTCCall(ctx, call)
	if err != nil {
		wa.UserLogin.Log.Warn().
			Err(err).
			Str("call_id", call.ID()).
			Stringer("peer_jid", call.Peer()).
			Msg("Failed to announce incoming WhatsApp call over MatrixRTC")
	}
}

func (wa *WhatsAppClient) handleVOIPCallEnded(callID, reason string) {
	wa.clearWhatsAppRemoteHandRaises(callID)
	ctx := wa.UserLogin.Log.WithContext(withoutCancelOrBackground(wa.Main.Bridge.BackgroundCtx))
	log := wa.UserLogin.Log.With().Str("call_id", callID).Str("reason", reason).Logger()
	call, err := wa.Main.DB.MatrixRTCCall.Get(ctx, wa.UserLogin.ID, callID)
	if err != nil {
		log.Err(err).Msg("Failed to look up MatrixRTC call record after WhatsApp call ended")
		return
	} else if call == nil {
		return
	}
	if err = wa.clearMatrixRTCMembership(ctx, call); err != nil {
		log.Err(err).Msg("Failed to clear MatrixRTC membership after WhatsApp call ended")
	}
	endReason, lastError := matrixRTCFinalEndReason(call, reason)
	if err = wa.Main.DB.MatrixRTCCall.MarkEnded(ctx, wa.UserLogin.ID, callID, "ended", endReason, lastError, time.Now()); err != nil {
		log.Err(err).Msg("Failed to mark MatrixRTC call ended")
	}
}

func matrixRTCFinalEndReason(call *wadb.MatrixRTCCall, reason string) (string, string) {
	if call != nil && !call.EndedTS.IsZero() && (call.EndReason != "" || call.LastError != "") {
		if call.EndReason != "" {
			reason = call.EndReason
		}
		return reason, call.LastError
	}
	return reason, ""
}

func (wa *WhatsAppClient) announceIncomingMatrixRTCCall(ctx context.Context, call *meowcaller.Call) error {
	log := zerolog.Ctx(ctx).With().
		Str("call_id", call.ID()).
		Stringer("peer_jid", call.Peer()).
		Logger()
	peer := wa.matrixRTCAnnouncementPeer(ctx, call.Peer())
	portal, err := wa.Main.Bridge.GetPortalByKey(ctx, wa.makeWAPortalKey(peer))
	if err != nil {
		return err
	}
	if portal == nil || portal.MXID == "" {
		log.Debug().Msg("No existing Matrix portal room for incoming MatrixRTC call announcement")
		return nil
	}
	if wa.Main.Config.VOIP.MaxActiveCallsPerLogin > 0 {
		activeCalls, err := wa.Main.DB.MatrixRTCCall.GetActiveForLogin(ctx, wa.UserLogin.ID)
		if err != nil {
			return err
		}
		if len(activeCalls) >= wa.Main.Config.VOIP.MaxActiveCallsPerLogin {
			log.Warn().
				Int("active_call_count", len(activeCalls)).
				Int("max_active_calls", wa.Main.Config.VOIP.MaxActiveCallsPerLogin).
				Msg("Rejecting incoming WhatsApp call because the MatrixRTC active call limit was reached")
			return call.Reject()
		}
	}
	focus, err := voip.DiscoverLiveKitFocus(ctx, nil, wa.Main.Bridge.Matrix.ServerName(), wa.Main.Config.VOIP.MatrixRTC.LiveKitServiceURL)
	if err != nil {
		return err
	}
	intent, err := wa.matrixRTCParticipantIntent(ctx, peer)
	if err != nil {
		return err
	}

	now := time.Now()
	deviceID := voip.MatrixRTCDeviceID(string(wa.UserLogin.ID), call.ID())
	session := voip.MatrixRTCSession{
		UserID:    intent.GetMXID(),
		DeviceID:  deviceID,
		MemberID:  voip.MatrixRTCMemberID(intent.GetMXID(), deviceID),
		Intent:    matrixRTCCallIntent(call),
		Focus:     *focus,
		Created:   now,
		Expires:   matrixRTCMembershipLifetime,
		StickyKey: voip.MatrixRTCMemberID(intent.GetMXID(), deviceID),
	}
	record := &wadb.MatrixRTCCall{
		UserLoginID:           wa.UserLogin.ID,
		WACallID:              call.ID(),
		RoomID:                portal.MXID,
		PortalKey:             portal.PortalKey,
		PeerJID:               peer,
		Direction:             "incoming",
		MediaKind:             session.Intent,
		FocusType:             focus.Type,
		LiveKitServiceURL:     focus.LiveKitServiceURL,
		LiveKitRoom:           portal.MXID.String(),
		MatrixParticipantMXID: intent.GetMXID(),
		MatrixSessionID:       deviceID,
		AudioPolicy:           wa.Main.Config.VOIP.LiveKit.AudioUplinkPolicy,
		State:                 "ringing",
		CreatedTS:             now,
	}
	if err = wa.Main.DB.MatrixRTCCall.Put(ctx, record); err != nil {
		return err
	}
	if err = wa.sendMatrixRTCRing(ctx, intent, portal.MXID, call.ID(), &session); err != nil {
		_ = wa.Main.DB.MatrixRTCCall.MarkEnded(ctx, wa.UserLogin.ID, call.ID(), "ended", "matrixrtc_announce_failed", err.Error(), time.Now())
		return err
	}
	record.BridgeMembershipEventID = session.MembershipEventID
	if err = wa.Main.DB.MatrixRTCCall.Put(ctx, record); err != nil {
		return err
	}
	log.Info().
		Stringer("room_id", portal.MXID).
		Stringer("participant_mxid", intent.GetMXID()).
		Str("device_id", deviceID).
		Msg("Announced incoming WhatsApp call over MatrixRTC")
	return nil
}

func (wa *WhatsAppClient) matrixRTCAnnouncementPeer(ctx context.Context, peer types.JID) types.JID {
	peer = peer.ToNonAD()
	if peer.Server != types.HiddenUserServer {
		return peer
	}
	pn, err := wa.GetStore().LIDs.GetPNForLID(ctx, peer)
	if err != nil {
		zerolog.Ctx(ctx).Err(err).
			Stringer("lid", peer).
			Msg("Failed to get phone number for LID incoming MatrixRTC call")
		return peer
	} else if pn.IsEmpty() {
		return peer
	}
	pn = pn.ToNonAD()
	zerolog.Ctx(ctx).Debug().
		Stringer("lid", peer).
		Stringer("pn", pn).
		Msg("Using phone number portal for incoming MatrixRTC call from LID")
	return pn
}

func (wa *WhatsAppConnector) startOutboundMatrixRTCCall(ctx context.Context, portal *bridgev2.Portal, trigger voip.MatrixRTCEvent) error {
	if portal == nil {
		return nil
	}
	if portal.Receiver == "" {
		return fmt.Errorf("portal has no receiver login for outbound MatrixRTC call")
	}
	login, err := wa.Bridge.GetExistingUserLoginByID(ctx, portal.Receiver)
	if err != nil {
		return err
	} else if login == nil || login.Client == nil {
		return fmt.Errorf("receiver login %s not found for outbound MatrixRTC call", portal.Receiver)
	} else if !login.Client.IsLoggedIn() {
		return bridgev2.ErrNotLoggedIn
	}
	client, ok := login.Client.(*WhatsAppClient)
	if !ok || client == nil || client.VOIP == nil {
		return fmt.Errorf("receiver login %s has no WhatsApp VOIP manager", portal.Receiver)
	}
	return client.startOutboundMatrixRTCCall(ctx, portal, trigger)
}

func (wa *WhatsAppClient) startOutboundMatrixRTCCall(ctx context.Context, portal *bridgev2.Portal, trigger voip.MatrixRTCEvent) error {
	if wa.VOIP == nil || !wa.VOIP.Enabled() {
		return voip.ErrNotEnabled
	}
	peer, err := waid.ParsePortalID(portal.ID)
	if err != nil {
		return err
	}
	if !matrixRTCPortalSupportsWhatsAppCalls(peer) {
		return fmt.Errorf("MatrixRTC WhatsApp calls are not supported in %s portals", peer.Server)
	}
	mediaKind, downgradedMedia := matrixRTCOutboundMediaKind(trigger)
	if mediaKind == "" {
		return fmt.Errorf("outbound WhatsApp MatrixRTC calls only support audio/video, not %q", trigger.Intent)
	}
	if downgradedMedia {
		wa.UserLogin.Log.Warn().
			Stringer("room_id", trigger.RoomID).
			Str("requested_media_kind", trigger.Intent).
			Str("media_kind", mediaKind).
			Msg("Downgrading outbound MatrixRTC call media kind")
	}
	if mediaKind == "video" && !wa.Main.Config.VOIP.Video.Enabled {
		return fmt.Errorf("outbound WhatsApp MatrixRTC video calls require voip.video.enabled")
	}
	if wa.Main.Config.VOIP.MaxActiveCallsPerLogin > 0 {
		activeCalls, err := wa.Main.DB.MatrixRTCCall.GetActiveForLogin(ctx, wa.UserLogin.ID)
		if err != nil {
			return err
		}
		if len(activeCalls) >= wa.Main.Config.VOIP.MaxActiveCallsPerLogin {
			return fmt.Errorf("active MatrixRTC call limit reached for login %s", wa.UserLogin.ID)
		}
	}
	focus, err := wa.matrixRTCLiveKitFocusForTrigger(ctx, trigger)
	if err != nil {
		return err
	}
	intent, err := wa.matrixRTCParticipantIntent(ctx, peer)
	if err != nil {
		return err
	}

	var call *meowcaller.Call
	if peer.Server == types.GroupServer {
		call, err = wa.VOIP.DialGroupByID(ctx, peer.ToNonAD().String(), mediaKind == "video")
	} else {
		call, err = wa.VOIP.Dial(ctx, peer.ToNonAD().String(), mediaKind == "video")
	}
	if err != nil {
		return err
	}
	now := time.Now()
	deviceID := voip.MatrixRTCDeviceID(string(wa.UserLogin.ID), call.ID())
	record := &wadb.MatrixRTCCall{
		UserLoginID:               wa.UserLogin.ID,
		WACallID:                  call.ID(),
		RoomID:                    trigger.RoomID,
		PortalKey:                 portal.PortalKey,
		PeerJID:                   peer,
		Direction:                 "outgoing",
		MediaKind:                 mediaKind,
		FocusType:                 focus.Type,
		LiveKitServiceURL:         focus.LiveKitServiceURL,
		LiveKitRoom:               trigger.RoomID.String(),
		MatrixParticipantMXID:     intent.GetMXID(),
		MatrixSessionID:           deviceID,
		SelectedPublisherID:       matrixRTCTriggerParticipantID(trigger),
		SelectedMembershipEventID: trigger.EventID,
		AudioPolicy:               wa.Main.Config.VOIP.LiveKit.AudioUplinkPolicy,
		State:                     "joining_livekit",
		CreatedTS:                 now,
	}
	if err = wa.Main.DB.MatrixRTCCall.Put(ctx, record); err != nil {
		_ = call.Hangup()
		return err
	}
	session := &voip.MatrixRTCSession{
		UserID:    intent.GetMXID(),
		DeviceID:  deviceID,
		MemberID:  voip.MatrixRTCMemberID(intent.GetMXID(), deviceID),
		Intent:    mediaKind,
		Focus:     *focus,
		Created:   now,
		Expires:   matrixRTCMembershipLifetime,
		StickyKey: voip.MatrixRTCMemberID(intent.GetMXID(), deviceID),
	}
	if err = wa.sendMatrixRTCMembership(ctx, intent, trigger.RoomID, session); err != nil {
		return wa.failMatrixRTCActivation(ctx, record, "matrixrtc_membership_failed", err)
	}
	record.BridgeMembershipEventID = session.MembershipEventID
	if err = wa.connectOutboundMatrixRTCCall(ctx, record, trigger); err != nil {
		wa.UserLogin.Log.Warn().
			Err(err).
			Str("call_id", call.ID()).
			Stringer("room_id", trigger.RoomID).
			Stringer("peer_jid", peer).
			Msg("Failed to connect outbound MatrixRTC call to LiveKit")
		return err
	}
	wa.UserLogin.Log.Info().
		Str("call_id", call.ID()).
		Stringer("room_id", trigger.RoomID).
		Stringer("peer_jid", peer).
		Stringer("matrix_participant_mxid", trigger.Sender).
		Msg("Started outbound WhatsApp call from MatrixRTC")
	return nil
}

func (wa *WhatsAppClient) sendMatrixRTCRing(ctx context.Context, intent bridgev2.MatrixAPI, roomID id.RoomID, waCallID string, session *voip.MatrixRTCSession) error {
	now := time.Now()
	notificationMode := wa.Main.Config.VOIP.MatrixRTC.NotificationEventCompat
	if matrixRTCCompatAllowsModern(notificationMode) {
		resp, err := sendMatrixRTCMessage(ctx, intent, roomID, voip.RTCNotificationEventType(), voip.BuildRTCNotificationContent(now, matrixRTCRingLifetime, session.Intent), 0)
		if err != nil {
			return err
		}
		if resp != nil {
			session.NotificationEventID = resp.EventID
		}
	}
	if matrixRTCCompatAllowsLegacy(notificationMode) {
		_, err := sendMatrixRTCMessage(ctx, intent, roomID, voip.LegacyCallNotifyEventType(), voip.BuildLegacyCallNotifyContent(waCallID, session.Intent), 0)
		if err != nil {
			return err
		}
	}
	return wa.sendMatrixRTCMembership(ctx, intent, roomID, session)
}

func (wa *WhatsAppClient) sendMatrixRTCMembership(ctx context.Context, intent bridgev2.MatrixAPI, roomID id.RoomID, session *voip.MatrixRTCSession) error {
	now := time.Now()
	membershipMode := wa.Main.Config.VOIP.MatrixRTC.MembershipEventCompat
	modernMessageSent := false
	if matrixRTCCompatAllowsModern(membershipMode) {
		content := voip.BuildRTCMembershipContent(*session)
		resp, err := sendMatrixRTCMessage(ctx, intent, roomID, voip.RTCMembershipEventType(event.MessageEventType), content, matrixRTCStickyDuration)
		if err != nil {
			return err
		}
		if resp != nil {
			session.MembershipEventID = resp.EventID
		}
		modernMessageSent = true
		stateKey := voip.MatrixRTCStateKey(session.UserID, session.DeviceID)
		stateResp, err := intent.SendState(ctx, roomID, voip.RTCMembershipEventType(event.StateEventType), stateKey, &event.Content{Raw: content}, now)
		if err != nil {
			wa.UserLogin.Log.Warn().
				Err(err).
				Stringer("room_id", roomID).
				Str("state_key", stateKey).
				Msg("Failed to send MatrixRTC membership state event after sticky message membership")
		} else if session.MembershipEventID == "" && stateResp != nil {
			session.MembershipEventID = stateResp.EventID
		}
	}
	if matrixRTCCompatAllowsLegacy(membershipMode) {
		resp, err := intent.SendState(ctx, roomID, voip.GroupCallMemberEventType(), "", &event.Content{Raw: voip.BuildLegacyCallMemberContent(*session)}, now)
		if err != nil {
			if modernMessageSent {
				wa.UserLogin.Log.Warn().
					Err(err).
					Stringer("room_id", roomID).
					Msg("Failed to send legacy MatrixRTC membership state event after modern membership")
				return nil
			}
			return err
		}
		if session.MembershipEventID == "" && resp != nil {
			session.MembershipEventID = resp.EventID
		}
	}
	return nil
}

func (wa *WhatsAppClient) clearMatrixRTCMembership(ctx context.Context, call *wadb.MatrixRTCCall) error {
	if call.RoomID == "" || call.MatrixParticipantMXID == "" {
		return nil
	}
	intent := wa.matrixRTCIntentForMXID(ctx, call.MatrixParticipantMXID)
	if intent == nil {
		intent = wa.Main.Bridge.Bot
	}
	stickyKey := voip.MatrixRTCMemberID(call.MatrixParticipantMXID, call.MatrixSessionID)
	emptyContent := voip.EmptyMatrixRTCContent(stickyKey)
	now := time.Now()
	membershipMode := wa.Main.Config.VOIP.MatrixRTC.MembershipEventCompat
	modernMessageSent := false
	if matrixRTCCompatAllowsModern(membershipMode) {
		if _, err := sendMatrixRTCMessage(ctx, intent, call.RoomID, voip.RTCMembershipEventType(event.MessageEventType), emptyContent, matrixRTCStickyDuration); err != nil {
			return err
		}
		modernMessageSent = true
		stateKey := voip.MatrixRTCStateKey(call.MatrixParticipantMXID, call.MatrixSessionID)
		if _, err := intent.SendState(ctx, call.RoomID, voip.RTCMembershipEventType(event.StateEventType), stateKey, &event.Content{Raw: map[string]any{}}, now); err != nil {
			wa.UserLogin.Log.Warn().
				Err(err).
				Stringer("room_id", call.RoomID).
				Str("state_key", stateKey).
				Msg("Failed to clear MatrixRTC membership state event after sticky message cleanup")
		}
	}
	if matrixRTCCompatAllowsLegacy(membershipMode) {
		if _, err := intent.SendState(ctx, call.RoomID, voip.GroupCallMemberEventType(), "", &event.Content{Raw: map[string]any{}}, now); err != nil {
			if modernMessageSent {
				wa.UserLogin.Log.Warn().
					Err(err).
					Stringer("room_id", call.RoomID).
					Msg("Failed to clear legacy MatrixRTC membership state event after modern cleanup")
				return nil
			}
			return err
		}
	}
	return nil
}

func (wa *WhatsAppConnector) cleanupFailedOutboundMatrixRTCStart(ctx context.Context, trigger voip.MatrixRTCEvent) error {
	if trigger.RoomID == "" || wa.Bridge == nil || wa.Bridge.Bot == nil {
		return nil
	}
	intent := wa.Bridge.Bot
	now := time.Now()
	membershipMode := wa.Config.VOIP.MatrixRTC.MembershipEventCompat

	if trigger.Kind == voip.MatrixRTCEventKindRTCMembership && matrixRTCCompatAllowsModern(membershipMode) {
		emptyContent := voip.EmptyMatrixRTCContent(matrixRTCTriggerStickyKey(trigger))
		if _, err := sendMatrixRTCMessage(ctx, intent, trigger.RoomID, voip.RTCMembershipEventType(event.MessageEventType), emptyContent, matrixRTCStickyDuration); err != nil {
			return err
		}
		stateKey := matrixRTCTriggerStateKey(trigger)
		if stateKey != "" {
			if _, err := intent.SendState(ctx, trigger.RoomID, voip.RTCMembershipEventType(event.StateEventType), stateKey, &event.Content{Raw: map[string]any{}}, now); err != nil {
				return err
			}
		}
	}

	if trigger.Kind == voip.MatrixRTCEventKindGroupCallMember && matrixRTCCompatAllowsLegacy(membershipMode) {
		if _, err := intent.SendState(ctx, trigger.RoomID, voip.GroupCallMemberEventType(), trigger.StateKey, &event.Content{Raw: map[string]any{}}, now); err != nil {
			return err
		}
	}
	return nil
}

func (wa *WhatsAppClient) activateMatrixRTCCall(ctx context.Context, call *wadb.MatrixRTCCall, trigger voip.MatrixRTCEvent) error {
	if call == nil {
		return nil
	}
	call.State = "joining_livekit"
	call.LastError = ""
	call.SelectedPublisherID = matrixRTCTriggerParticipantID(trigger)
	call.SelectedMembershipEventID = trigger.EventID
	if err := wa.Main.DB.MatrixRTCCall.Put(ctx, call); err != nil {
		return err
	}
	authResp, err := wa.requestMatrixRTCLiveKitAuth(ctx, call, trigger)
	if err != nil {
		return err
	}
	if err = wa.VOIP.BridgeCallToLiveKit(ctx, call.WACallID, authResp, call.SelectedPublisherID); err != nil {
		return wa.failMatrixRTCActivation(ctx, call, "livekit_bridge_failed", err)
	}
	now := time.Now()
	call.State = "active"
	call.JoinedTS = now
	call.AnsweredTS = now
	if authResp.RoomName != "" {
		call.LiveKitRoom = authResp.RoomName
	}
	if err = wa.Main.DB.MatrixRTCCall.Put(ctx, call); err != nil {
		return err
	}
	wa.UserLogin.Log.Info().
		Str("call_id", call.WACallID).
		Stringer("room_id", call.RoomID).
		Stringer("trigger_sender", trigger.Sender).
		Msg("Activated MatrixRTC LiveKit bridge for WhatsApp call")
	return nil
}

func (wa *WhatsAppClient) connectOutboundMatrixRTCCall(ctx context.Context, call *wadb.MatrixRTCCall, trigger voip.MatrixRTCEvent) error {
	authResp, err := wa.requestMatrixRTCLiveKitAuth(ctx, call, trigger)
	if err != nil {
		return err
	}
	if err = wa.VOIP.BridgeCallToLiveKit(ctx, call.WACallID, authResp, call.SelectedPublisherID); err != nil {
		return wa.failMatrixRTCActivation(ctx, call, "livekit_bridge_failed", err)
	}
	now := time.Now()
	call.State = "active"
	call.JoinedTS = now
	if authResp.RoomName != "" {
		call.LiveKitRoom = authResp.RoomName
	}
	return wa.Main.DB.MatrixRTCCall.Put(ctx, call)
}

func (wa *WhatsAppClient) requestMatrixRTCLiveKitAuth(ctx context.Context, call *wadb.MatrixRTCCall, trigger voip.MatrixRTCEvent) (*voip.LiveKitAuthResponse, error) {
	intent := wa.matrixRTCIntentForMXID(ctx, call.MatrixParticipantMXID)
	openIDToken, err := requestMatrixOpenIDToken(ctx, intent)
	if err != nil {
		return nil, wa.failMatrixRTCActivation(ctx, call, "matrix_openid_failed", err)
	}
	if matrixRTCCompatAllowsLegacy(wa.Main.Config.VOIP.MatrixRTC.MembershipEventCompat) {
		authResp, err := voip.RequestLegacyLiveKitAuth(ctx, nil, call.LiveKitServiceURL, matrixRTCLegacyLiveKitAuthRequest(call, openIDToken))
		if err != nil {
			return nil, wa.failMatrixRTCActivation(ctx, call, "livekit_auth_failed", err)
		}
		return authResp, nil
	}
	authResp, err := voip.RequestLiveKitAuth(ctx, nil, call.LiveKitServiceURL, matrixRTCLiveKitAuthRequest(call, openIDToken))
	if err != nil {
		return nil, wa.failMatrixRTCActivation(ctx, call, "livekit_auth_failed", err)
	}
	return authResp, nil
}

func (wa *WhatsAppClient) failMatrixRTCActivation(ctx context.Context, call *wadb.MatrixRTCCall, reason string, err error) error {
	_ = wa.Main.DB.MatrixRTCCall.MarkEnded(ctx, call.UserLoginID, call.WACallID, "ended", reason, err.Error(), time.Now())
	if wa.VOIP != nil {
		wa.VOIP.HandleMatrixRTCCallEvent(ctx, voip.MatrixRTCEvent{
			Kind:   voip.MatrixRTCEventKindRTCDecline,
			RoomID: call.RoomID,
		}, call.WACallID)
	}
	return err
}

func matrixRTCLiveKitAuthRequest(call *wadb.MatrixRTCCall, openIDToken voip.MatrixOpenIDToken) voip.LiveKitAuthRequest {
	if call == nil {
		return voip.LiveKitAuthRequest{OpenIDToken: openIDToken}
	}
	return voip.LiveKitAuthRequest{
		RoomID:      call.RoomID.String(),
		SlotID:      voip.MatrixRTCDefaultSlotID,
		OpenIDToken: openIDToken,
		Member:      matrixRTCLiveKitAuthMember(call),
	}
}

func matrixRTCLegacyLiveKitAuthRequest(call *wadb.MatrixRTCCall, openIDToken voip.MatrixOpenIDToken) voip.LegacyLiveKitAuthRequest {
	if call == nil {
		return voip.LegacyLiveKitAuthRequest{OpenIDToken: openIDToken}
	}
	return voip.LegacyLiveKitAuthRequest{
		Room:        call.RoomID.String(),
		OpenIDToken: openIDToken,
		DeviceID:    call.MatrixSessionID,
	}
}

func matrixRTCLiveKitAuthMember(call *wadb.MatrixRTCCall) *voip.LiveKitAuthMember {
	if call == nil {
		return nil
	}
	return &voip.LiveKitAuthMember{
		ID:              voip.MatrixRTCMemberID(call.MatrixParticipantMXID, call.MatrixSessionID),
		ClaimedDeviceID: call.MatrixSessionID,
		ClaimedUserID:   call.MatrixParticipantMXID.String(),
	}
}

func (wa *WhatsAppClient) matrixRTCLiveKitFocusForTrigger(ctx context.Context, trigger voip.MatrixRTCEvent) (*voip.Focus, error) {
	for _, focus := range trigger.FociPreferred {
		if focus.Type == "livekit" && focus.LiveKitServiceURL != "" {
			focusCopy := focus
			return &focusCopy, nil
		}
	}
	return voip.DiscoverLiveKitFocus(ctx, nil, wa.Main.Bridge.Matrix.ServerName(), wa.Main.Config.VOIP.MatrixRTC.LiveKitServiceURL)
}

func (wa *WhatsAppClient) matrixRTCParticipantIntent(ctx context.Context, peer types.JID) (bridgev2.MatrixAPI, error) {
	mode := strings.ToLower(wa.Main.Config.VOIP.MatrixRTC.ParticipantMode)
	if mode == "" || mode == "whatsapp_ghost" {
		if ghostID := waid.MakeUserID(peer); ghostID != "" {
			ghost, err := wa.Main.Bridge.GetGhostByID(ctx, ghostID)
			if err != nil {
				return nil, err
			}
			if ghost != nil && ghost.Intent != nil {
				return ghost.Intent, nil
			}
		}
	}
	return wa.Main.Bridge.Bot, nil
}

func (wa *WhatsAppClient) matrixRTCIntentForMXID(ctx context.Context, mxid id.UserID) bridgev2.MatrixAPI {
	if mxid == "" || mxid == wa.Main.Bridge.Bot.GetMXID() {
		return wa.Main.Bridge.Bot
	}
	if ghost, err := wa.Main.Bridge.GetGhostByMXID(ctx, mxid); err == nil && ghost != nil && ghost.Intent != nil {
		return ghost.Intent
	}
	return wa.Main.Bridge.Bot
}

func matrixRTCPortalSupportsWhatsAppCalls(peer types.JID) bool {
	switch peer.Server {
	case types.DefaultUserServer, types.HiddenUserServer, types.GroupServer:
		return true
	default:
		return false
	}
}

func matrixRTCCallIntent(call *meowcaller.Call) string {
	if call != nil && call.IsVideo() {
		return "video"
	}
	return "audio"
}

func matrixRTCOutboundMediaKind(trigger voip.MatrixRTCEvent) (mediaKind string, downgraded bool) {
	switch trigger.Intent {
	case "", "audio":
		return "audio", false
	case "video":
		return "video", false
	default:
		return "", false
	}
}

func matrixRTCTriggerParticipantID(trigger voip.MatrixRTCEvent) string {
	if trigger.Sender == "" {
		return ""
	}
	deviceID := trigger.SessionID
	if deviceID == "" {
		deviceID = trigger.DeviceID
	}
	return voip.MatrixRTCMemberID(trigger.Sender, deviceID)
}

func matrixRTCTriggerStateKey(trigger voip.MatrixRTCEvent) string {
	if trigger.StateKey != "" {
		return trigger.StateKey
	}
	deviceID := trigger.SessionID
	if deviceID == "" {
		deviceID = trigger.DeviceID
	}
	if trigger.Sender == "" {
		return deviceID
	}
	return voip.MatrixRTCStateKey(trigger.Sender, deviceID)
}

func matrixRTCTriggerStickyKey(trigger voip.MatrixRTCEvent) string {
	if stickyKey, ok := trigger.Raw["sticky_key"].(string); ok && stickyKey != "" {
		return stickyKey
	}
	if stickyKey, ok := trigger.Raw["msc4354_sticky_key"].(string); ok && stickyKey != "" {
		return stickyKey
	}
	return matrixRTCTriggerParticipantID(trigger)
}

func sendMatrixRTCMessage(ctx context.Context, intent bridgev2.MatrixAPI, roomID id.RoomID, eventType event.Type, raw map[string]any, sticky time.Duration) (*mautrix.RespSendEvent, error) {
	if asIntent, ok := intent.(*mxbridge.ASIntent); ok {
		return asIntent.Matrix.SendMessageEvent(ctx, roomID, eventType, &event.Content{Raw: raw}, mautrix.ReqSendEvent{
			UnstableStickyDuration: sticky,
			DontEncrypt:            true,
		})
	}
	return intent.SendMessage(ctx, roomID, eventType, &event.Content{Raw: raw}, nil)
}

func requestMatrixOpenIDToken(ctx context.Context, intent bridgev2.MatrixAPI) (voip.MatrixOpenIDToken, error) {
	asIntent, ok := intent.(*mxbridge.ASIntent)
	if !ok {
		return voip.MatrixOpenIDToken{}, fmt.Errorf("matrix intent %T does not support OpenID token requests", intent)
	}
	if asIntent.Matrix == nil || asIntent.Matrix.Client == nil {
		return voip.MatrixOpenIDToken{}, fmt.Errorf("matrix intent %T has no Matrix client", intent)
	}
	resp, err := asIntent.Matrix.Client.RequestOpenIDToken(ctx)
	if err != nil {
		return voip.MatrixOpenIDToken{}, err
	}
	return voip.MatrixOpenIDToken{
		AccessToken:      resp.AccessToken,
		TokenType:        resp.TokenType,
		MatrixServerName: resp.MatrixServerName,
		ExpiresIn:        resp.ExpiresIn,
	}, nil
}

func matrixRTCCompatAllowsModern(mode string) bool {
	switch strings.ToLower(mode) {
	case "legacy", "legacy_only", "msc3401", "org.matrix.msc3401.call.member":
		return false
	default:
		return true
	}
}

func matrixRTCCompatAllowsLegacy(mode string) bool {
	switch strings.ToLower(mode) {
	case "modern", "modern_only", "msc4143", "org.matrix.msc4143.rtc.member", "none", "off", "false", "disabled":
		return false
	default:
		return true
	}
}
