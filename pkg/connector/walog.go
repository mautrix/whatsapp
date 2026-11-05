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
	"github.com/rs/zerolog"
	"go.mau.fi/util/exsync"
	waBinary "go.mau.fi/whatsmeow/binary"
	waLog "go.mau.fi/whatsmeow/util/log"
)

type whatsmeowLogger struct {
	waLog.Logger
	pings *exsync.Set[string]
}

func newWhatsmeowLogger(log zerolog.Logger) waLog.Logger {
	return &whatsmeowLogger{Logger: waLog.Zerolog(log), pings: exsync.NewSet[string]()}
}

func (l *whatsmeowLogger) Sub(module string) waLog.Logger {
	sub := l.Logger.Sub(module)
	switch module {
	case "Send", "Recv":
		return &nodeLogger{Logger: sub, pings: l.pings, send: module == "Send"}
	default:
		return sub
	}
}

type nodeLogger struct {
	waLog.Logger
	pings *exsync.Set[string]
	send  bool
}

func (l *nodeLogger) Debugf(msg string, args ...any) {
	if len(args) == 1 {
		if node, ok := args[0].(*waBinary.Node); ok && node.Tag == "iq" && node.Content == nil {
			id, _ := node.Attrs["id"].(string)
			if l.send && node.Attrs["xmlns"] == "w:p" && node.Attrs["type"] == "get" {
				l.pings.Add(id)
				return
			} else if !l.send && node.Attrs["type"] == "result" && l.pings.Pop(id) {
				return
			}
		}
	}
	l.Logger.Debugf(msg, args...)
}
