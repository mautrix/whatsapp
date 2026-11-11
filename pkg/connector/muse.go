package connector

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"go.mau.fi/util/exerrors"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waAICommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/commands"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/event"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

type museImage struct {
	DirectPath string `json:"direct_path"`
	EncHash    []byte `json:"file_enc_sha256_b64"`
	Hash       []byte `json:"file_sha256_b64"`
	Key        []byte `json:"media_key_b64"`
	Length     int    `json:"file_length"`
}

func (wa *WhatsAppClient) resyncWASARootSecrets(ctx context.Context) {
	defer func() {
		v := recover()
		if v != nil {
			zerolog.Ctx(ctx).Err(exerrors.RecoverToError(v)).
				Bytes("stack", debug.Stack()).
				Msg("Error resyncing WASA root secrets")
		}
	}()
	wa.wasaResyncLock.Lock()
	defer wa.wasaResyncLock.Unlock()
	if wa.offlineSyncWaiter.Load() != nil || !wa.Client.IsConnected() {
		return
	}
	meta := wa.UserLogin.Metadata.(*waid.UserLoginMetadata)
	if meta.WASAResynced {
		return
	}
	log := zerolog.Ctx(ctx)
	log.Info().Msg("Resyncing WASA root secrets for existing login")
	success := true
	if err := wa.Client.FetchAppState(ctx, appstate.WAPatchRegularHigh, true, false); err != nil {
		log.Err(err).Msg("Failed to resync WASA root secrets")
		// Mark the sync as completed on errors too, because some get permanently stuck (mismatching LTHash,
		// expired patch blobs), and a failed full sync already requests app state recovery from the phone.
		if ctx.Err() != nil {
			return
		}
		success = false
	}
	meta.WASAResynced = true
	if err := wa.UserLogin.Save(ctx); err != nil {
		meta.WASAResynced = false
		log.Err(err).Msg("Failed to save WASA resync completion")
		return
	}
	if success {
		log.Info().Msg("Completed WASA root secret resync")
	} else {
		log.Info().Msg("Marked WASA root resync as done despite errors")
	}
}

func (wa *WhatsAppClient) requestMuseProfile(ctx context.Context) {
	defer func() {
		v := recover()
		if v != nil {
			zerolog.Ctx(ctx).Err(exerrors.RecoverToError(v)).
				Bytes("stack", debug.Stack()).
				Msg("Error requesting Muse profile")
		}
	}()
	if !wa.museProfileLock.TryLock() {
		return
	}
	defer wa.museProfileLock.Unlock()
	if wa.offlineSyncWaiter.Load() != nil || !wa.Client.IsConnected() || time.Since(wa.lastMuseProfileRequest) < 15*time.Minute {
		return
	}
	portal, err := wa.Main.Bridge.DB.Portal.GetByKey(ctx, wa.makeWAPortalKey(types.MuseJID))
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to check Muse profile")
		return
	} else if portal != nil && portal.NameIsCustom && portal.Name != "" && portal.NameSet && museAvatarReady(portal) {
		return
	}
	rootID, err := wa.GetStore().ChatSettings.GetWASARootSecretID(ctx, types.MuseJID)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to check Muse root secret")
		return
	} else if rootID == "" {
		return
	}
	zerolog.Ctx(ctx).Info().Msg("Sending request to sync Muse profile")
	wa.lastMuseProfileRequest = time.Now()
	_, err = wa.Client.SendMessage(ctx, types.MuseJID, &waE2E.Message{
		ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_AI_METADATA_OPERATION.Enum(),
			AiMetadataOperation: &waAICommon.AIMetadataOperation{
				HatchMetadataSync: &waAICommon.HatchMetadataSync{
					Data:        []byte(`{"version":1,"type":"req","payload":{"method":"channel.bootstrap","params":{"sections":["agent.status","identity.updated","hitl.snapshot"]}}}`),
					TimestampMS: proto.Int64(time.Now().UnixMilli()),
					RequestID:   proto.String(uuid.NewString()),
				},
			},
		},
	})
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to request Muse profile")
	}
}

func museAvatarReady(portal *database.Portal) bool {
	expectedHash, err := base64.StdEncoding.DecodeString(string(portal.AvatarID))
	return err != nil || len(expectedHash) != 32 ||
		(portal.AvatarMXC != "" && portal.AvatarHash == [32]byte(expectedHash))
}

type museApprovalRecord struct {
	ApprovalID         string `json:"approval_id"`
	Decision           string `json:"decision"`
	Status             string `json:"status"`
	RequestExpiresAtMS int64  `json:"request_expires_at_ms"`
	DecisionOptions    []struct {
		Kind      string `json:"kind"`
		LabelText string `json:"label_text"`
	} `json:"decision_options"`
	Display struct {
		PermissionQuestion struct {
			Text string `json:"text"`
		} `json:"permission_question"`
		PurposeSummary string `json:"purpose_summary"`
		SummaryTitle   string `json:"summary_title"`
	} `json:"display"`
}

func (wa *WhatsAppClient) handleMuseMetadata(ctx context.Context, sync *waAICommon.HatchMetadataSync) bool {
	data := sync.GetData()
	var msg struct {
		Type    string `json:"type"`
		Status  string `json:"status"`
		Payload struct {
			Body      json.RawMessage `json:"body"`
			Event     string          `json:"event"`
			Timestamp int64           `json:"ts_ms"`
			Payload   json.RawMessage `json:"payload"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to decode Muse metadata")
		return true
	} else if msg.Type == "res" {
		if ch, ok := wa.museRequests.Pop(sync.GetRequestID()); ok {
			ch <- museResponse{Status: msg.Status, Body: msg.Payload.Body}
		}
		return true
	} else if msg.Type != "event" {
		return true
	}
	switch msg.Payload.Event {
	case "identity.updated":
		return wa.handleMuseIdentity(ctx, msg.Payload.Payload)
	case "hitl.approval_record":
		return wa.handleMuseApproval(ctx, msg.Payload.Payload, msg.Payload.Timestamp)
	default:
		return true
	}
}

func (wa *WhatsAppClient) handleMuseIdentity(ctx context.Context, data json.RawMessage) bool {
	var identity struct {
		Name   string `json:"name"`
		Avatar struct {
			Image museImage `json:"secure_image"`
		} `json:"avatar"`
	}
	if len(data) == 0 {
		return true
	} else if err := json.Unmarshal(data, &identity); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to decode Muse identity")
		return true
	} else if identity.Name == "" {
		return true
	}
	name := identity.Name
	avatar := wa.museAvatar(identity.Avatar.Image)
	return wa.UserLogin.QueueRemoteEvent(&simplevent.ChatInfoChange{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatInfoChange,
			PortalKey:    wa.makeWAPortalKey(types.MuseJID),
			CreatePortal: true,
			PreHandleFunc: func(_ context.Context, portal *bridgev2.Portal) {
				if avatar != nil && !museAvatarReady(portal.Portal) {
					portal.AvatarSet = false
				}
			},
		},
		ChatInfoChange: &bridgev2.ChatInfoChange{
			ChatInfo: &bridgev2.ChatInfo{
				Name:                       &name,
				Avatar:                     avatar,
				ExcludeChangesFromTimeline: true,
			},
		},
	}).Success
}

func (wa *WhatsAppClient) handleMuseApproval(ctx context.Context, data json.RawMessage, tsMS int64) bool {
	var wrapper struct {
		Lifecycle      string              `json:"lifecycle"`
		Record         *museApprovalRecord `json:"record"`
		ApprovalRecord *museApprovalRecord `json:"approval_record"`
		Approval       *museApprovalRecord `json:"approval"`
		museApprovalRecord
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to decode Muse approval record")
		return true
	}
	record := &wrapper.museApprovalRecord
	if wrapper.Record != nil {
		record = wrapper.Record
	} else if wrapper.ApprovalRecord != nil {
		record = wrapper.ApprovalRecord
	} else if wrapper.Approval != nil {
		record = wrapper.Approval
	}
	approvalID := strings.TrimSpace(record.ApprovalID)
	if approvalID == "" {
		return true
	}
	status := strings.TrimSpace(wrapper.Lifecycle)
	if status == "" {
		status = strings.TrimSpace(record.Status)
	}
	resolution := strings.TrimSpace(record.Decision)
	if resolution == "" && status != "" && status != "pending" {
		resolution = status
	}
	log := zerolog.Ctx(ctx).With().Str("approval_id", approvalID).Str("resolution", resolution).Logger()
	ts := time.Now()
	if tsMS > 0 {
		ts = time.UnixMilli(tsMS)
	}
	meta := &waid.MuseApprovalMeta{
		ID:       approvalID,
		Question: museApprovalQuestion(record),
		Summary:  strings.TrimSpace(record.Display.PurposeSummary),
	}
	for _, opt := range record.DecisionOptions {
		if kind := strings.TrimSpace(opt.Kind); kind != "" {
			meta.Options = append(meta.Options, kind)
		}
	}
	evt := &simplevent.Message[*waid.MuseApprovalMeta]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: wa.makeWAPortalKey(types.MuseJID),
			Sender:    wa.makeEventSender(ctx, types.MuseJID),
			Timestamp: ts,
		},
		Data:               meta,
		ID:                 waid.MakeFakeMessageID(types.MuseJID, types.MuseJID, "approval-"+approvalID),
		ConvertMessageFunc: convertMuseApproval,
	}
	if resolution != "" {
		evt.Type = bridgev2.RemoteEventEdit
		evt.TargetMessage = evt.ID
		evt.ConvertEditFunc = func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, data *waid.MuseApprovalMeta) (*bridgev2.ConvertedEdit, error) {
			msgMeta := existing[0].Metadata.(*waid.MessageMetadata)
			if prev := msgMeta.MuseApproval; prev != nil {
				if data.Question == "" {
					data.Question = prev.Question
				}
				if data.Summary == "" {
					data.Summary = prev.Summary
				}
			}
			data.Options = nil
			data.Resolved = true
			msgMeta.MuseApproval = data
			return &bridgev2.ConvertedEdit{
				ModifiedParts: []*bridgev2.ConvertedEditPart{{
					Part:    existing[0],
					Type:    event.EventMessage,
					Content: museApprovalContent(portal, data, museApprovalResolutionText(resolution)),
				}},
			}, nil
		}
	} else if len(meta.Options) == 0 {
		log.Debug().Msg("Ignoring Muse approval record without decision options")
		return true
	} else if record.RequestExpiresAtMS > 0 && time.UnixMilli(record.RequestExpiresAtMS).Before(time.Now()) {
		log.Debug().Msg("Ignoring expired Muse approval record")
		return true
	} else {
		evt.CreatePortal = true
	}
	log.Debug().Msg("Received Muse approval record")
	return wa.UserLogin.QueueRemoteEvent(evt).Success
}

func museApprovalResolutionText(resolution string) string {
	switch strings.ToLower(resolution) {
	case "allow", "allowed", "allow_once", "allow_session", "allow_today", "allow_always", "approve", "approved":
		return "Allowed."
	case "deny", "denied", "deny_always", "reject", "rejected", "decline", "declined":
		return "Denied."
	case "expired", "timeout", "timed_out":
		return "This request expired."
	default:
		return "No longer waiting for approval."
	}
}

func museApprovalQuestion(record *museApprovalRecord) string {
	if question := strings.TrimSpace(record.Display.PermissionQuestion.Text); question != "" {
		return question
	}
	return strings.TrimSpace(record.Display.SummaryTitle)
}

func museApprovalContent(portal *bridgev2.Portal, meta *waid.MuseApprovalMeta, footer string) *event.MessageEventContent {
	lines := make([]string, 0, 3)
	if meta.Question != "" {
		assistant := portal.Name
		if assistant == "" {
			assistant = "Muse"
		}
		lines = append(lines, strings.ReplaceAll(meta.Question, "{assistant}", assistant))
	} else {
		lines = append(lines, "Muse is asking for your approval.")
	}
	if meta.Summary != "" {
		lines = append(lines, meta.Summary)
	}
	lines = append(lines, footer)
	return &event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    strings.Join(lines, "\n\n"),
	}
}

func convertMuseApproval(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, meta *waid.MuseApprovalMeta) (*bridgev2.ConvertedMessage, error) {
	prefix := portal.Bridge.Config.CommandPrefix
	footer := fmt.Sprintf("Reply to this message with `%s approval-allow` or `%s approval-deny`, or use the WhatsApp app.", prefix, prefix)
	return &bridgev2.ConvertedMessage{
		Parts: []*bridgev2.ConvertedMessagePart{{
			Type:       event.EventMessage,
			Content:    museApprovalContent(portal, meta, footer),
			DBMetadata: &waid.MessageMetadata{MuseApproval: meta},
		}},
	}, nil
}

type museResponse struct {
	Status string
	Body   json.RawMessage
}

func (wa *WhatsAppClient) decideMuseApproval(ctx context.Context, approvalID, decision string) error {
	payload, err := json.Marshal(map[string]any{
		"version": 1,
		"type":    "req",
		"payload": map[string]any{
			"method": "hitl.approval.decide",
			"params": map[string]any{
				"approval_id": approvalID,
				"decision":    decision,
			},
		},
	})
	if err != nil {
		return err
	}
	requestID := uuid.NewString()
	ch := make(chan museResponse, 1)
	wa.museRequests.Set(requestID, ch)
	defer wa.museRequests.Delete(requestID)
	_, err = wa.Client.SendMessage(ctx, types.MuseJID, &waE2E.Message{
		ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_AI_METADATA_OPERATION.Enum(),
			AiMetadataOperation: &waAICommon.AIMetadataOperation{
				HatchMetadataSync: &waAICommon.HatchMetadataSync{
					Data:        payload,
					TimestampMS: proto.Int64(time.Now().UnixMilli()),
					RequestID:   proto.String(requestID),
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to send decision: %w", err)
	}
	select {
	case resp := <-ch:
		if resp.Status != "ok" {
			var body struct {
				ErrorMessage string `json:"error_message"`
				ErrorCode    string `json:"error_code"`
			}
			_ = json.Unmarshal(resp.Body, &body)
			if body.ErrorMessage != "" {
				return fmt.Errorf("Muse rejected the decision: %s", body.ErrorMessage)
			}
			return fmt.Errorf("Muse rejected the decision (%s)", cmp.Or(body.ErrorCode, resp.Status))
		}
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("Muse didn't confirm the decision")
	case <-ctx.Done():
		return ctx.Err()
	}
}

var cmdMuseApprovalAllow = &commands.FullHandler{
	Func: fnMuseApproval,
	Name: "approval-allow",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Allow a pending Muse request. This can only be used in reply to a Muse approval message.",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

var cmdMuseApprovalDeny = &commands.FullHandler{
	Func: fnMuseApproval,
	Name: "approval-deny",
	Help: commands.HelpMeta{
		Section:     commands.HelpSectionChats,
		Description: "Deny a pending Muse request. This can only be used in reply to a Muse approval message.",
	},
	RequiresLogin:  true,
	RequiresPortal: true,
}

func museDecisionKind(options []string, allow bool) string {
	if !allow {
		if slices.Contains(options, "deny") {
			return "deny"
		}
		return ""
	}
	for _, kind := range []string{"allow_once", "allow"} {
		if slices.Contains(options, kind) {
			return kind
		}
	}
	return ""
}

func fnMuseApproval(ce *commands.Event) {
	allow := ce.Command == "approval-allow"
	if len(ce.ReplyTo) == 0 {
		ce.Reply("Reply to a Muse approval message when using this command.")
	} else if message, err := ce.Bridge.DB.Message.GetPartByMXID(ce.Ctx, ce.ReplyTo); err != nil {
		ce.Log.Err(err).Stringer("reply_to_mxid", ce.ReplyTo).Msg("Failed to get reply target event for Muse approval command")
		ce.Reply("Failed to get reply event")
	} else if message == nil || message.Room != ce.Portal.PortalKey {
		ce.Reply("Reply event not found")
	} else if meta := message.Metadata.(*waid.MessageMetadata).MuseApproval; meta == nil {
		ce.Reply("That doesn't look like a Muse approval message.")
	} else if meta.Resolved {
		ce.Reply("This request has already been decided.")
	} else if kind := museDecisionKind(meta.Options, allow); kind == "" {
		ce.Reply("Muse doesn't offer that choice for this request. Use the WhatsApp app instead.")
	} else if login := ce.Bridge.GetCachedUserLoginByID(ce.Portal.Receiver); login == nil || login.UserMXID != ce.User.MXID {
		ce.Reply("Login not found")
	} else if !login.Client.IsLoggedIn() {
		ce.Reply("Not logged in")
	} else if err = login.Client.(*WhatsAppClient).decideMuseApproval(ce.Ctx, meta.ID, kind); err != nil {
		ce.Log.Err(err).Str("approval_id", meta.ID).Str("decision", kind).Msg("Failed to send Muse approval decision")
		ce.Reply("%v. Use the WhatsApp app instead.", err)
	} else {
		ce.React("✅")
	}
}

func (wa *WhatsAppClient) museAvatar(image museImage) *bridgev2.Avatar {
	if image.DirectPath == "" || image.Length <= 0 || image.Length > 5<<20 {
		return nil
	}
	if len(image.EncHash) != 32 || len(image.Hash) != 32 || len(image.Key) != 32 {
		return nil
	}
	return &bridgev2.Avatar{
		ID: networkid.AvatarID(base64.StdEncoding.EncodeToString(image.Hash)),
		Get: func(ctx context.Context) ([]byte, error) {
			data, err := wa.Client.DownloadMediaWithPath(ctx, image.DirectPath, image.EncHash, image.Hash, image.Key, whatsmeow.MediaImage, "image", false)
			if err != nil {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("Failed to download Muse avatar")
				return nil, fmt.Errorf("failed to download Muse avatar: %w", err)
			}
			return data, nil
		},
	}
}
