// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2022 Tulir Asokan, Sumner Evans
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

package main

import (
	"bytes"
	"encoding/json"
	"net/http"

	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

const SegmentURL = "https://api.segment.io/v1/track"

type Segment struct {
	segmentKey string
	log        log.Logger
	client     *http.Client
}

func NewSegment(segmentKey string, parentLogger log.Logger) *Segment {
	return &Segment{
		segmentKey: segmentKey,
		log:        parentLogger.Sub("Segment"),
		client:     &http.Client{},
	}
}

func (segment *Segment) track(userID id.UserID, event string, properties map[string]interface{}) error {
	data := map[string]interface{}{
		"userId":     userID,
		"event":      event,
		"properties": properties,
	}
	json_data, err := json.Marshal(data)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", SegmentURL, bytes.NewBuffer(json_data))
	if err != nil {
		return err
	}
	req.SetBasicAuth(segment.segmentKey, "")
	resp, err := segment.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func (segment *Segment) Track(userID id.UserID, event string, properties ...map[string]interface{}) {
	if segment.segmentKey == "" {
		return
	}
	if len(properties) > 1 {
		segment.log.Fatalf("Track should be called with at most one property map")
	}

	go (func() error {
		props := map[string]interface{}{}
		if len(properties) > 0 {
			props = properties[0]
		}
		props["bridge"] = "whatsapp"
		err := segment.track(userID, event, props)
		if err != nil {
			segment.log.Errorf("Error tracking %s: %v+", event, err)
			return err
		}
		segment.log.Debug("Tracked ", event)
		return nil
	})()
}
