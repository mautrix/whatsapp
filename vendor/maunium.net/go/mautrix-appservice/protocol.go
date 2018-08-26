package appservice

import (
	"encoding/json"
	"net/http"
	"maunium.net/go/gomatrix"
)

// EventList contains a list of events.
type EventList struct {
	Events []*gomatrix.Event `json:"events"`
}

// EventListener is a function that receives events.
type EventListener func(event *gomatrix.Event)

// WriteBlankOK writes a blank OK message as a reply to a HTTP request.
func WriteBlankOK(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{}"))
}

// Respond responds to a HTTP request with a JSON object.
func Respond(w http.ResponseWriter, data interface{}) error {
	dataStr, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(dataStr))
	return err
}

// Error represents a Matrix protocol error.
type Error struct {
	HTTPStatus int       `json:"-"`
	ErrorCode  ErrorCode `json:"errcode"`
	Message    string    `json:"message"`
}

func (err Error) Write(w http.ResponseWriter) {
	w.WriteHeader(err.HTTPStatus)
	Respond(w, &err)
}

// ErrorCode is the machine-readable code in an Error.
type ErrorCode string

// Native ErrorCodes
const (
	ErrForbidden ErrorCode = "M_FORBIDDEN"
	ErrUnknown   ErrorCode = "M_UNKNOWN"
)

// Custom ErrorCodes
const (
	ErrNoTransactionID ErrorCode = "NET.MAUNIUM.NO_TRANSACTION_ID"
	ErrNoBody          ErrorCode = "NET.MAUNIUM.NO_REQUEST_BODY"
	ErrInvalidJSON     ErrorCode = "NET.MAUNIUM.INVALID_JSON"
)
