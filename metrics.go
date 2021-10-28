// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2021 Tulir Asokan
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
	"context"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "maunium.net/go/maulogger/v2"

	"go.mau.fi/whatsmeow/types"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"maunium.net/go/mautrix-whatsapp/database"
)

type MetricsHandler struct {
	db     *database.Database
	server *http.Server
	log    log.Logger

	running      bool
	ctx          context.Context
	stopRecorder func()

	matrixEventHandling     *prometheus.HistogramVec
	whatsappMessageAge      prometheus.Histogram
	whatsappMessageHandling *prometheus.HistogramVec
	countCollection         prometheus.Histogram
	disconnections          *prometheus.CounterVec
	puppetCount             prometheus.Gauge
	userCount               prometheus.Gauge
	messageCount            prometheus.Gauge
	portalCount             *prometheus.GaugeVec
	encryptedGroupCount     prometheus.Gauge
	encryptedPrivateCount   prometheus.Gauge
	unencryptedGroupCount   prometheus.Gauge
	unencryptedPrivateCount prometheus.Gauge

	connected          prometheus.Gauge
	connectedState     map[string]bool
	connectedStateLock sync.Mutex
	loggedIn           prometheus.Gauge
	loggedInState      map[string]bool
	loggedInStateLock  sync.Mutex
}

func NewMetricsHandler(address string, log log.Logger, db *database.Database) *MetricsHandler {
	portalCount := promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "whatsapp_portals_total",
		Help: "Number of portal rooms on Matrix",
	}, []string{"type", "encrypted"})
	return &MetricsHandler{
		db:      db,
		server:  &http.Server{Addr: address, Handler: promhttp.Handler()},
		log:     log,
		running: false,

		matrixEventHandling: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name: "matrix_event",
			Help: "Time spent processing Matrix events",
		}, []string{"event_type"}),
		whatsappMessageAge: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "remote_event_age",
			Help:    "Age of messages received from WhatsApp",
			Buckets: []float64{1, 2, 3, 5, 7.5, 10, 20, 30, 60},
		}),
		whatsappMessageHandling: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name: "remote_event",
			Help: "Time spent processing WhatsApp messages",
		}, []string{"message_type"}),
		countCollection: promauto.NewHistogram(prometheus.HistogramOpts{
			Name: "whatsapp_count_collection",
			Help: "Time spent collecting the whatsapp_*_total metrics",
		}),
		disconnections: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "whatsapp_disconnections",
			Help: "Number of times a Matrix user has been disconnected from WhatsApp",
		}, []string{"user_id"}),
		puppetCount: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "whatsapp_puppets_total",
			Help: "Number of WhatsApp users bridged into Matrix",
		}),
		userCount: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "whatsapp_users_total",
			Help: "Number of Matrix users using the bridge",
		}),
		messageCount: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "whatsapp_messages_total",
			Help: "Number of messages bridged",
		}),
		portalCount:             portalCount,
		encryptedGroupCount:     portalCount.With(prometheus.Labels{"type": "group", "encrypted": "true"}),
		encryptedPrivateCount:   portalCount.With(prometheus.Labels{"type": "private", "encrypted": "true"}),
		unencryptedGroupCount:   portalCount.With(prometheus.Labels{"type": "group", "encrypted": "false"}),
		unencryptedPrivateCount: portalCount.With(prometheus.Labels{"type": "private", "encrypted": "false"}),

		loggedIn: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "bridge_logged_in",
			Help: "Users logged into the bridge",
		}),
		loggedInState: make(map[string]bool),
		connected: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "bridge_connected",
			Help: "Bridge users connected to WhatsApp",
		}),
		connectedState: make(map[string]bool),
	}
}

func noop() {}

func (mh *MetricsHandler) TrackMatrixEvent(eventType event.Type) func() {
	if !mh.running {
		return noop
	}
	start := time.Now()
	return func() {
		duration := time.Now().Sub(start)
		mh.matrixEventHandling.
			With(prometheus.Labels{"event_type": eventType.Type}).
			Observe(duration.Seconds())
	}
}

func (mh *MetricsHandler) TrackWhatsAppMessage(timestamp time.Time, messageType string) func() {
	if !mh.running {
		return noop
	}

	start := time.Now()
	return func() {
		duration := time.Now().Sub(start)
		mh.whatsappMessageHandling.
			With(prometheus.Labels{"message_type": messageType}).
			Observe(duration.Seconds())
		mh.whatsappMessageAge.Observe(time.Now().Sub(timestamp).Seconds())
	}
}

func (mh *MetricsHandler) TrackDisconnection(userID id.UserID) {
	if !mh.running {
		return
	}
	mh.disconnections.With(prometheus.Labels{"user_id": string(userID)}).Inc()
}

func (mh *MetricsHandler) TrackLoginState(jid types.JID, loggedIn bool) {
	if !mh.running {
		return
	}
	mh.loggedInStateLock.Lock()
	defer mh.loggedInStateLock.Unlock()
	currentVal, ok := mh.loggedInState[jid.User]
	if !ok || currentVal != loggedIn {
		mh.loggedInState[jid.User] = loggedIn
		if loggedIn {
			mh.loggedIn.Inc()
		} else {
			mh.loggedIn.Dec()
		}
	}
}

func (mh *MetricsHandler) TrackConnectionState(jid types.JID, connected bool) {
	if !mh.running {
		return
	}
	mh.connectedStateLock.Lock()
	defer mh.connectedStateLock.Unlock()
	currentVal, ok := mh.connectedState[jid.User]
	if !ok || currentVal != connected {
		mh.connectedState[jid.User] = connected
		if connected {
			mh.connected.Inc()
		} else {
			mh.connected.Dec()
		}
	}
}

func (mh *MetricsHandler) updateStats() {
	start := time.Now()
	var puppetCount int
	err := mh.db.QueryRowContext(mh.ctx, "SELECT COUNT(*) FROM puppet").Scan(&puppetCount)
	if err != nil {
		mh.log.Warnln("Failed to scan number of puppets:", err)
	} else {
		mh.puppetCount.Set(float64(puppetCount))
	}

	var userCount int
	err = mh.db.QueryRowContext(mh.ctx, `SELECT COUNT(*) FROM "user"`).Scan(&userCount)
	if err != nil {
		mh.log.Warnln("Failed to scan number of users:", err)
	} else {
		mh.userCount.Set(float64(userCount))
	}

	var messageCount int
	err = mh.db.QueryRowContext(mh.ctx, "SELECT COUNT(*) FROM message").Scan(&messageCount)
	if err != nil {
		mh.log.Warnln("Failed to scan number of messages:", err)
	} else {
		mh.messageCount.Set(float64(messageCount))
	}

	var encryptedGroupCount, encryptedPrivateCount, unencryptedGroupCount, unencryptedPrivateCount int
	err = mh.db.QueryRowContext(mh.ctx, `
			SELECT
				COUNT(CASE WHEN jid LIKE '%@g.us' AND encrypted THEN 1 END) AS encrypted_group_portals,
				COUNT(CASE WHEN jid LIKE '%@s.whatsapp.net' AND encrypted THEN 1 END) AS encrypted_private_portals,
				COUNT(CASE WHEN jid LIKE '%@g.us' AND NOT encrypted THEN 1 END) AS unencrypted_group_portals,
				COUNT(CASE WHEN jid LIKE '%@s.whatsapp.net' AND NOT encrypted THEN 1 END) AS unencrypted_private_portals
			FROM portal WHERE mxid<>''
		`).Scan(&encryptedGroupCount, &encryptedPrivateCount, &unencryptedGroupCount, &unencryptedPrivateCount)
	if err != nil {
		mh.log.Warnln("Failed to scan number of portals:", err)
	} else {
		mh.encryptedGroupCount.Set(float64(encryptedGroupCount))
		mh.encryptedPrivateCount.Set(float64(encryptedPrivateCount))
		mh.unencryptedGroupCount.Set(float64(unencryptedGroupCount))
		mh.unencryptedPrivateCount.Set(float64(encryptedPrivateCount))
	}
	mh.countCollection.Observe(time.Now().Sub(start).Seconds())
}

func (mh *MetricsHandler) startUpdatingStats() {
	defer func() {
		err := recover()
		if err != nil {
			mh.log.Fatalfln("Panic in metric updater: %v\n%s", err, string(debug.Stack()))
		}
	}()
	ticker := time.Tick(10 * time.Second)
	for {
		mh.updateStats()
		select {
		case <-mh.ctx.Done():
			return
		case <-ticker:
		}
	}
}

func (mh *MetricsHandler) Start() {
	mh.running = true
	mh.ctx, mh.stopRecorder = context.WithCancel(context.Background())
	go mh.startUpdatingStats()
	err := mh.server.ListenAndServe()
	mh.running = false
	if err != nil && err != http.ErrServerClosed {
		mh.log.Fatalln("Error in metrics listener:", err)
	}
}

func (mh *MetricsHandler) Stop() {
	if !mh.running {
		return
	}
	mh.stopRecorder()
	err := mh.server.Close()
	if err != nil {
		mh.log.Errorln("Error closing metrics listener:", err)
	}
}
