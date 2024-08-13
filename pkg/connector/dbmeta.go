package connector

import (
	"maunium.net/go/mautrix/bridgev2/database"
)

func (wa *WhatsAppConnector) GetDBMetaTypes() database.MetaTypes {
	println("GetDBMetaTypes unimplemented")
	return database.MetaTypes{
		Ghost:     nil,
		Message:   nil,
		Reaction:  nil,
		Portal:    nil,
		UserLogin: func() any { return &UserLoginMetadata{} },
	}
}

type UserLoginMetadata struct {
	WADeviceID uint16 `json:"wa_device_id"`
	//TODO: Add phone last ping/seen
}
