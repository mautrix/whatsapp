// mautrix-meta - A Matrix-Facebook Messenger and Instagram DM puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
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

package msgconv

import (
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/format"

	"go.mau.fi/mautrix-whatsapp/pkg/connector/wadb"
)

type AnimatedStickerConfig struct {
	Target string `yaml:"target"`
	Args   struct {
		Width  int `yaml:"width"`
		Height int `yaml:"height"`
		FPS    int `yaml:"fps"`
	} `yaml:"args"`
}

type MessageConverter struct {
	Bridge                *bridgev2.Bridge
	DB                    *wadb.Database
	MaxFileSize           int64
	HTMLParser            *format.HTMLParser
	AnimatedStickerConfig AnimatedStickerConfig
	FetchURLPreviews      bool
	ExtEvPolls            bool
	DisableViewOnce       bool
	DirectMedia           bool
	OldMediaSuffix        string
}

func New(br *bridgev2.Bridge) *MessageConverter {
	mc := &MessageConverter{
		Bridge:      br,
		MaxFileSize: 50 * 1024 * 1024,
	}
	mc.HTMLParser = &format.HTMLParser{
		PillConverter: mc.convertPill,
		Newline:       "\n",
		TabsToSpaces:  4,
		BoldConverter: func(text string, ctx format.Context) string {
			return "*" + text + "*"
		},
		ItalicConverter: func(text string, ctx format.Context) string {
			return "_" + text + "_"
		},
		StrikethroughConverter: func(text string, ctx format.Context) string {
			return "~" + text + "~"
		},
		MonospaceConverter: func(text string, ctx format.Context) string {
			return "`" + text + "`"
		},
		MonospaceBlockConverter: func(code, language string, ctx format.Context) string {
			return "```\n" + code + "\n```"
		},
	}
	return mc
}
