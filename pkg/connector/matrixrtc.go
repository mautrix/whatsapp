package connector

import (
	"context"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/matrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-whatsapp/pkg/connector/voip"
)

const (
	matrixRTCHealthcheckTimeout       = 15 * time.Second
	matrixRTCOutboundStartDedupWindow = 30 * time.Second
)

func withoutCancelOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

func (wa *WhatsAppConnector) initMatrixRTCEventHooks() {
	matrixConnector, ok := wa.Bridge.Matrix.(*matrix.Connector)
	if !ok || matrixConnector.EventProcessor == nil {
		wa.Bridge.Log.Debug().Msg("Matrix connector does not expose an event processor for MatrixRTC hooks")
		return
	}
	for _, evtType := range voip.SupportedMatrixRTCEventTypes() {
		matrixConnector.EventProcessor.On(evtType, wa.handleMatrixRTCEvent)
	}
	wa.Bridge.Log.Debug().Int("event_type_count", len(voip.SupportedMatrixRTCEventTypes())).Msg("Registered MatrixRTC event hooks")
}

func (wa *WhatsAppConnector) startMatrixRTCHealthcheck() {
	if !wa.Config.VOIP.Enabled || !wa.Config.VOIP.Diagnostics.HealthcheckFocusOnStartup {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(withoutCancelOrBackground(wa.Bridge.BackgroundCtx), matrixRTCHealthcheckTimeout)
		defer cancel()
		focus, err := voip.DiscoverLiveKitFocus(ctx, nil, wa.Bridge.Matrix.ServerName(), wa.Config.VOIP.MatrixRTC.LiveKitServiceURL)
		log := wa.Bridge.Log.With().Str("component", "voip_healthcheck").Logger()
		if err != nil {
			event := log.Warn()
			if wa.Config.VOIP.MatrixRTC.RequireLiveKitFocus {
				event = log.Error()
			}
			event.Err(err).Msg("MatrixRTC LiveKit focus healthcheck failed")
			return
		}
		log.Info().
			Str("focus_type", focus.Type).
			Str("livekit_service_url", focus.LiveKitServiceURL).
			Msg("MatrixRTC LiveKit focus healthcheck passed")
	}()
}

func (wa *WhatsAppConnector) handleMatrixRTCEvent(ctx context.Context, evt *event.Event) {
	if !wa.Config.VOIP.Enabled {
		return
	}
	parsed, ok := voip.ParseMatrixRTCEvent(evt)
	if !ok {
		return
	}

	log := zerolog.Ctx(ctx).With().
		Str("matrixrtc_kind", string(parsed.Kind)).
		Str("matrix_event_type", parsed.Type.Type).
		Stringer("matrix_room_id", parsed.RoomID).
		Stringer("matrix_sender", parsed.Sender).
		Str("matrix_call_id", parsed.CallID).
		Logger()

	if parsed.Sender == wa.Bridge.Bot.GetMXID() || wa.Bridge.IsGhostMXID(parsed.Sender) {
		log.Debug().Msg("Ignoring MatrixRTC event sent by the bridge")
		return
	}
	if !wa.Bridge.Config.Permissions.Get(parsed.Sender).SendEvents {
		log.Debug().Msg("Dropping MatrixRTC event from user with no permission to send events")
		wa.Bridge.Matrix.SendMessageStatus(ctx, &bridgev2.ErrNoPermissionToInteract, bridgev2.StatusEventInfoFromEvent(evt))
		return
	}

	portal, err := wa.Bridge.GetPortalByMXID(ctx, parsed.RoomID)
	if err != nil {
		log.Err(err).Msg("Failed to look up portal for MatrixRTC event")
		return
	} else if portal == nil {
		log.Debug().Msg("Ignoring MatrixRTC event outside a bridged portal")
		return
	}

	activeCalls, err := wa.DB.MatrixRTCCall.GetActiveInRoom(ctx, parsed.RoomID)
	if err != nil {
		log.Err(err).Msg("Failed to look up active MatrixRTC calls for room")
		return
	} else if len(activeCalls) == 0 {
		if !shouldStartOutboundMatrixRTCCall(evt, parsed, wa.Config.VOIP.MatrixRTC.MembershipEventCompat) {
			log.Debug().Msg("Ignoring MatrixRTC event without active bridged calls in the room")
			return
		}
		if !wa.reserveMatrixRTCOutboundStart(parsed.RoomID.String()) {
			log.Debug().Msg("Ignoring duplicate outbound MatrixRTC start in dedupe window")
			return
		}
		if err = wa.startOutboundMatrixRTCCall(ctx, portal, parsed); err != nil {
			log.Err(err).Msg("Failed to start outbound WhatsApp call from MatrixRTC event")
			if cleanupErr := wa.cleanupFailedOutboundMatrixRTCStart(ctx, parsed); cleanupErr != nil {
				log.Err(cleanupErr).Msg("Failed to clean up failed outbound MatrixRTC event")
			}
		}
		return
	}

	var matched, handled, activated, ended int
	for _, activeCall := range activeCalls {
		matched++
		login, err := wa.Bridge.GetExistingUserLoginByID(ctx, activeCall.UserLoginID)
		callLog := log.With().
			Str("wa_call_id", activeCall.WACallID).
			Str("user_login_id", string(activeCall.UserLoginID)).
			Stringer("matrix_participant_mxid", activeCall.MatrixParticipantMXID).
			Str("matrix_session_id", activeCall.MatrixSessionID).
			Logger()
		if err != nil {
			callLog.Err(err).Msg("Failed to look up WhatsApp login for MatrixRTC call")
			continue
		} else if login == nil {
			callLog.Debug().Msg("MatrixRTC call references a missing WhatsApp login")
			continue
		}
		client, ok := login.Client.(*WhatsAppClient)
		if !ok || client == nil || client.VOIP == nil {
			callLog.Debug().Msg("WhatsApp login has no VOIP manager for MatrixRTC event")
			continue
		}
		handled++
		endedCalls := client.VOIP.HandleMatrixRTCCallEvent(ctx, parsed, activeCall.WACallID)
		if shouldEndMatrixRTCCallFromMembership(parsed, activeCall.SelectedPublisherID) {
			endedCalls += client.VOIP.HandleMatrixRTCCallEvent(ctx, voip.MatrixRTCEvent{
				Kind:   voip.MatrixRTCEventKindRTCDecline,
				RoomID: parsed.RoomID,
				Sender: parsed.Sender,
				CallID: parsed.CallID,
			}, activeCall.WACallID)
		}
		ended += endedCalls
		if shouldActivateMatrixRTCCall(parsed, activeCall.State) {
			if err = client.activateMatrixRTCCall(ctx, activeCall, parsed); err != nil {
				callLog.Err(err).Msg("Failed to activate MatrixRTC LiveKit bridge for WhatsApp call")
			} else {
				activated++
			}
		}
		if endedCalls > 0 {
			err = wa.DB.MatrixRTCCall.MarkEnded(ctx, activeCall.UserLoginID, activeCall.WACallID, "ended", string(parsed.Kind), "", time.Now())
			if err != nil {
				callLog.Err(err).Msg("Failed to mark MatrixRTC call ended after MatrixRTC event")
			}
		}
	}
	log.Debug().
		Int("active_call_count", len(activeCalls)).
		Int("matched_call_count", matched).
		Int("handled_call_count", handled).
		Int("activated_call_count", activated).
		Int("ended_call_count", ended).
		Msg("Handled MatrixRTC event for active bridged calls")
}

func shouldActivateMatrixRTCCall(evt voip.MatrixRTCEvent, callState string) bool {
	if callState != "ringing" {
		return false
	}
	switch evt.Kind {
	case voip.MatrixRTCEventKindRTCMembership, voip.MatrixRTCEventKindGroupCallMember:
		return voip.MatrixRTCEventHasJoinContent(evt)
	default:
		return false
	}
}

func shouldStartOutboundMatrixRTCCall(evt *event.Event, parsed voip.MatrixRTCEvent, membershipCompat string) bool {
	if parsed.Type.Class != event.StateEventType || !voip.MatrixRTCEventHasJoinContent(parsed) {
		return false
	}
	switch parsed.Kind {
	case voip.MatrixRTCEventKindRTCMembership:
		if !matrixRTCCompatAllowsModern(membershipCompat) {
			return false
		}
	case voip.MatrixRTCEventKindGroupCallMember:
		if !matrixRTCCompatAllowsLegacy(membershipCompat) {
			return false
		}
	default:
		return false
	}
	return !matrixRTCPrevContentHasJoinContent(evt)
}

func shouldEndMatrixRTCCallFromMembership(evt voip.MatrixRTCEvent, selectedParticipantID string) bool {
	switch evt.Kind {
	case voip.MatrixRTCEventKindRTCMembership, voip.MatrixRTCEventKindGroupCallMember:
	default:
		return false
	}
	if selectedParticipantID == "" || voip.MatrixRTCEventHasJoinContent(evt) {
		return false
	}
	return matrixRTCEventMatchesParticipant(evt, selectedParticipantID)
}

func matrixRTCEventMatchesParticipant(evt voip.MatrixRTCEvent, participantID string) bool {
	if participantID == "" {
		return false
	}
	if matrixRTCTriggerParticipantID(evt) == participantID {
		return true
	}
	if evt.StateKey == "" || evt.Sender == "" {
		return false
	}
	if legacyID := legacyMatrixRTCParticipantIDFromStateKey(evt.Sender, evt.StateKey); legacyID == participantID {
		return true
	}
	if modernID := modernMatrixRTCParticipantIDFromStateKey(evt.Sender, evt.StateKey); modernID == participantID {
		return true
	}
	return false
}

func legacyMatrixRTCParticipantIDFromStateKey(sender id.UserID, stateKey string) string {
	prefix := "_" + string(sender) + "_"
	const suffix = "_m.call"
	if !strings.HasPrefix(stateKey, prefix) || !strings.HasSuffix(stateKey, suffix) {
		return ""
	}
	deviceID := strings.TrimSuffix(strings.TrimPrefix(stateKey, prefix), suffix)
	if deviceID == "" {
		return ""
	}
	return voip.MatrixRTCMemberID(sender, deviceID)
}

func modernMatrixRTCParticipantIDFromStateKey(sender id.UserID, stateKey string) string {
	prefix := string(sender) + "_"
	if !strings.HasPrefix(stateKey, prefix) {
		return ""
	}
	deviceID := strings.TrimPrefix(stateKey, prefix)
	if deviceID == "" {
		return ""
	}
	return voip.MatrixRTCMemberID(sender, deviceID)
}

func (wa *WhatsAppConnector) reserveMatrixRTCOutboundStart(roomID string) bool {
	if roomID == "" {
		return false
	}
	now := time.Now()
	wa.matrixRTCOutboundStartLock.Lock()
	defer wa.matrixRTCOutboundStartLock.Unlock()
	if wa.matrixRTCOutboundStartExpires == nil {
		wa.matrixRTCOutboundStartExpires = make(map[string]time.Time)
	}
	for trackedRoomID, expires := range wa.matrixRTCOutboundStartExpires {
		if !expires.After(now) {
			delete(wa.matrixRTCOutboundStartExpires, trackedRoomID)
		}
	}
	if expires := wa.matrixRTCOutboundStartExpires[roomID]; expires.After(now) {
		return false
	}
	wa.matrixRTCOutboundStartExpires[roomID] = now.Add(matrixRTCOutboundStartDedupWindow)
	return true
}

func matrixRTCPrevContentHasJoinContent(evt *event.Event) bool {
	if evt == nil || evt.Unsigned.PrevContent == nil {
		return false
	}
	prevEvt := *evt
	prevEvt.Content = *evt.Unsigned.PrevContent
	prevEvt.Unsigned.PrevContent = nil
	parsedPrev, ok := voip.ParseMatrixRTCEvent(&prevEvt)
	return ok && voip.MatrixRTCEventHasJoinContent(parsedPrev)
}
