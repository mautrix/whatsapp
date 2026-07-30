package connector

import (
	"context"
	"strings"

	"github.com/purpshell/meowcaller"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow/types"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/connector/voip"
	"go.mau.fi/mautrix-whatsapp/pkg/connector/wadb"
)

func isMatrixRTCCallControlEvent(evt voip.MatrixRTCEvent) bool {
	switch evt.Kind {
	case voip.MatrixRTCEventKindCallReaction, voip.MatrixRTCEventKindHandRaise, voip.MatrixRTCEventKindRedaction:
		return true
	default:
		return false
	}
}

func matrixRTCControlEventMatchesCall(evt voip.MatrixRTCEvent, call *wadb.MatrixRTCCall) bool {
	if call == nil || !matrixRTCEventSenderMatchesPublisher(evt.Sender.String(), call.SelectedPublisherID) {
		return false
	}
	switch evt.Kind {
	case voip.MatrixRTCEventKindCallReaction, voip.MatrixRTCEventKindHandRaise:
		return evt.RelatesToEventID != "" && evt.RelatesToEventID == call.SelectedMembershipEventID
	case voip.MatrixRTCEventKindRedaction:
		return evt.Redacts != "" && evt.Redacts == call.SelectedHandRaiseEventID
	default:
		return false
	}
}

func matrixRTCEventSenderMatchesPublisher(sender, publisherID string) bool {
	if sender == "" || publisherID == "" {
		return false
	}
	return publisherID == sender || strings.HasPrefix(publisherID, sender+":")
}

func (wa *WhatsAppConnector) handleMatrixRTCCallControlEvent(
	ctx context.Context,
	evt voip.MatrixRTCEvent,
	activeCalls []*wadb.MatrixRTCCall,
	log zerolog.Logger,
) {
	for _, activeCall := range activeCalls {
		if !matrixRTCControlEventMatchesCall(evt, activeCall) {
			continue
		}
		login, err := wa.Bridge.GetExistingUserLoginByID(ctx, activeCall.UserLoginID)
		if err != nil {
			log.Err(err).Str("wa_call_id", activeCall.WACallID).Msg("Failed to resolve login for MatrixRTC call control")
			continue
		}
		if login == nil {
			continue
		}
		client, ok := login.Client.(*WhatsAppClient)
		if !ok || client == nil || client.VOIP == nil {
			continue
		}

		switch evt.Kind {
		case voip.MatrixRTCEventKindCallReaction:
			if evt.RelationType != event.RelReference {
				continue
			}
			emoji, supported := voip.NormalizeWhatsAppCallReaction(evt.ReactionEmoji)
			if !supported {
				log.Debug().Str("emoji", evt.ReactionEmoji).Msg("Ignoring unsupported MatrixRTC call reaction")
				continue
			}
			if err = client.VOIP.SendReaction(activeCall.WACallID, emoji); err != nil {
				log.Warn().Err(err).Str("wa_call_id", activeCall.WACallID).Str("emoji", emoji).Msg("Failed to send MatrixRTC reaction to WhatsApp")
			}
		case voip.MatrixRTCEventKindHandRaise:
			if evt.RelationType != event.RelAnnotation || evt.RelationKey != "🖐️" || evt.EventID == "" {
				continue
			}
			if activeCall.SelectedHandRaiseEventID != "" {
				continue
			}
			if err = client.VOIP.SetHandRaised(activeCall.WACallID, true); err != nil {
				log.Warn().Err(err).Str("wa_call_id", activeCall.WACallID).Msg("Failed to raise hand in WhatsApp call")
				continue
			}
			activeCall.SelectedHandRaiseEventID = evt.EventID
			if err = wa.DB.MatrixRTCCall.Put(ctx, activeCall); err != nil {
				log.Err(err).Str("wa_call_id", activeCall.WACallID).Msg("Failed to persist MatrixRTC hand raise")
				_ = client.VOIP.SetHandRaised(activeCall.WACallID, false)
			}
		case voip.MatrixRTCEventKindRedaction:
			if err = client.VOIP.SetHandRaised(activeCall.WACallID, false); err != nil {
				log.Warn().Err(err).Str("wa_call_id", activeCall.WACallID).Msg("Failed to lower hand in WhatsApp call")
				continue
			}
			activeCall.SelectedHandRaiseEventID = ""
			if err = wa.DB.MatrixRTCCall.Put(ctx, activeCall); err != nil {
				log.Err(err).Str("wa_call_id", activeCall.WACallID).Msg("Failed to persist MatrixRTC hand lowering")
				_ = client.VOIP.SetHandRaised(activeCall.WACallID, true)
			}
		}
	}
}

func (wa *WhatsAppClient) handleWhatsAppCallReaction(ctx context.Context, callID string, reaction meowcaller.CallReaction) {
	if reaction.Removed {
		return
	}
	emoji, supported := voip.NormalizeWhatsAppCallReaction(reaction.Emoji)
	if !supported {
		return
	}
	call, err := wa.Main.DB.MatrixRTCCall.Get(ctx, wa.UserLogin.ID, callID)
	if err != nil || call == nil || !call.EndedTS.IsZero() || call.BridgeMembershipEventID == "" {
		if err != nil {
			wa.UserLogin.Log.Err(err).Str("call_id", callID).Msg("Failed to load MatrixRTC call for WhatsApp reaction")
		}
		return
	}
	intent := wa.matrixRTCIntentForMXID(ctx, call.MatrixParticipantMXID)
	_, err = intent.SendMessage(ctx, call.RoomID, voip.ElementCallReactionEventType(), &event.Content{
		Raw: voip.BuildElementCallReactionContent(call.BridgeMembershipEventID, emoji),
	}, nil)
	if err != nil {
		wa.UserLogin.Log.Warn().Err(err).Str("call_id", callID).Str("emoji", emoji).Msg("Failed to bridge WhatsApp call reaction to MatrixRTC")
	}
}

func (wa *WhatsAppClient) handleWhatsAppHandRaise(ctx context.Context, callID string, state meowcaller.HandRaiseState) {
	if wa.isOwnWhatsAppCallParticipant(state.Participant) {
		return
	}
	wa.voipHandBridgeLock.Lock()
	defer wa.voipHandBridgeLock.Unlock()
	raised, changed := wa.updateWhatsAppRemoteHandRaise(callID, state)
	if !changed {
		return
	}
	rollback := func() {
		state.Raised = !state.Raised
		wa.updateWhatsAppRemoteHandRaise(callID, state)
	}
	call, err := wa.Main.DB.MatrixRTCCall.Get(ctx, wa.UserLogin.ID, callID)
	if err != nil || call == nil || !call.EndedTS.IsZero() || call.BridgeMembershipEventID == "" {
		rollback()
		if err != nil {
			wa.UserLogin.Log.Err(err).Str("call_id", callID).Msg("Failed to load MatrixRTC call for WhatsApp hand state")
		}
		return
	}
	intent := wa.matrixRTCIntentForMXID(ctx, call.MatrixParticipantMXID)
	if raised {
		if call.BridgeHandRaiseEventID != "" {
			return
		}
		resp, sendErr := intent.SendMessage(ctx, call.RoomID, event.EventReaction, &event.Content{
			Raw: voip.BuildElementCallHandRaiseContent(call.BridgeMembershipEventID),
		}, nil)
		if sendErr != nil {
			rollback()
			wa.UserLogin.Log.Warn().Err(sendErr).Str("call_id", callID).Msg("Failed to bridge WhatsApp hand raise to MatrixRTC")
			return
		}
		if resp != nil {
			call.BridgeHandRaiseEventID = resp.EventID
		}
		if call.BridgeHandRaiseEventID == "" {
			rollback()
			return
		}
		if err = wa.Main.DB.MatrixRTCCall.Put(ctx, call); err != nil {
			rollback()
			_, _ = intent.SendMessage(ctx, call.RoomID, event.EventRedaction, &event.Content{
				Parsed: &event.RedactionEventContent{Redacts: call.BridgeHandRaiseEventID},
			}, nil)
			wa.UserLogin.Log.Err(err).Str("call_id", callID).Msg("Failed to persist bridged WhatsApp hand raise")
		}
		return
	} else {
		if call.BridgeHandRaiseEventID == "" {
			return
		}
		handRaiseEventID := call.BridgeHandRaiseEventID
		call.BridgeHandRaiseEventID = ""
		if err = wa.Main.DB.MatrixRTCCall.Put(ctx, call); err != nil {
			rollback()
			wa.UserLogin.Log.Err(err).Str("call_id", callID).Msg("Failed to persist bridged WhatsApp hand lowering")
			return
		}
		_, sendErr := intent.SendMessage(ctx, call.RoomID, event.EventRedaction, &event.Content{
			Parsed: &event.RedactionEventContent{Redacts: handRaiseEventID},
		}, nil)
		if sendErr != nil {
			rollback()
			call.BridgeHandRaiseEventID = handRaiseEventID
			_ = wa.Main.DB.MatrixRTCCall.Put(ctx, call)
			wa.UserLogin.Log.Warn().Err(sendErr).Str("call_id", callID).Msg("Failed to bridge WhatsApp hand lowering to MatrixRTC")
			return
		}
	}
}

func (wa *WhatsAppClient) updateWhatsAppRemoteHandRaise(callID string, state meowcaller.HandRaiseState) (raised, changed bool) {
	if callID == "" || state.Participant.IsEmpty() {
		return false, false
	}
	participant := state.Participant.ToNonAD()
	wa.voipHandRaiseLock.Lock()
	defer wa.voipHandRaiseLock.Unlock()
	if wa.voipHandRaises == nil {
		wa.voipHandRaises = make(map[string]map[types.JID]bool)
	}
	hands := wa.voipHandRaises[callID]
	if hands == nil {
		hands = make(map[types.JID]bool)
		wa.voipHandRaises[callID] = hands
	}
	wasRaised := len(hands) > 0
	if state.Raised {
		hands[participant] = true
	} else {
		delete(hands, participant)
	}
	raised = len(hands) > 0
	if !raised {
		delete(wa.voipHandRaises, callID)
	}
	return raised, wasRaised != raised
}

func (wa *WhatsAppClient) clearWhatsAppRemoteHandRaises(callID string) {
	wa.voipHandBridgeLock.Lock()
	defer wa.voipHandBridgeLock.Unlock()
	wa.voipHandRaiseLock.Lock()
	delete(wa.voipHandRaises, callID)
	wa.voipHandRaiseLock.Unlock()
}

func (wa *WhatsAppClient) isOwnWhatsAppCallParticipant(participant types.JID) bool {
	if participant.IsEmpty() || wa.GetStore() == nil {
		return false
	}
	participant = participant.ToNonAD()
	return participant == wa.GetStore().GetLID().ToNonAD() ||
		participant == wa.GetStore().GetJID().ToNonAD()
}
