package voip

import (
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

var supportedWhatsAppCallReactions = map[string]string{
	"👍":  "thumbsup",
	"❤️": "generic",
	"😂":  "generic",
	"😮":  "generic",
	"😢":  "generic",
	"🙏":  "generic",
}

func ElementCallReactionEventType() event.Type {
	return event.Type{Type: EventTypeElementCallReaction, Class: event.MessageEventType}
}

func NormalizeWhatsAppCallReaction(emoji string) (string, bool) {
	if _, ok := supportedWhatsAppCallReactions[emoji]; ok {
		return emoji, true
	}
	return "", false
}

func ElementCallReactionName(emoji string) string {
	if name, ok := supportedWhatsAppCallReactions[emoji]; ok {
		return name
	}
	return "generic"
}

func BuildElementCallReactionContent(membershipEventID id.EventID, emoji string) map[string]any {
	return map[string]any{
		"m.relates_to": map[string]any{
			"rel_type": string(event.RelReference),
			"event_id": membershipEventID,
		},
		"emoji": emoji,
		"name":  ElementCallReactionName(emoji),
	}
}

func BuildElementCallHandRaiseContent(membershipEventID id.EventID) map[string]any {
	return map[string]any{
		"m.relates_to": map[string]any{
			"rel_type": string(event.RelAnnotation),
			"event_id": membershipEventID,
			"key":      "🖐️",
		},
	}
}
