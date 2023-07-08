module maunium.net/go/mautrix-whatsapp

go 1.19

require (
	github.com/chai2010/webp v1.1.1
	github.com/gorilla/mux v1.8.0
	github.com/gorilla/websocket v1.5.0
	github.com/lib/pq v1.10.9
	github.com/mattn/go-sqlite3 v1.14.17
	github.com/prometheus/client_golang v1.16.0
	github.com/rs/zerolog v1.29.1
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/tidwall/gjson v1.14.4
	go.mau.fi/whatsmeow v0.0.0-20230628202737-27f697dce47a
	golang.org/x/exp v0.0.0-20230626212559-97b1e661b5df
	golang.org/x/image v0.8.0
	golang.org/x/net v0.11.0
	google.golang.org/protobuf v1.31.0
	maunium.net/go/maulogger/v2 v2.4.1
	maunium.net/go/mautrix v0.15.4-0.20230707135424-4639f0c9c471
)

require (
	filippo.io/edwards25519 v1.0.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/golang/protobuf v1.5.3 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/mattn/go-colorable v0.1.12 // indirect
	github.com/mattn/go-isatty v0.0.14 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.4 // indirect
	github.com/prometheus/client_model v0.3.0 // indirect
	github.com/prometheus/common v0.42.0 // indirect
	github.com/prometheus/procfs v0.10.1 // indirect
	github.com/rogpeppe/go-internal v1.10.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/yuin/goldmark v1.5.4 // indirect
	go.mau.fi/libsignal v0.1.0 // indirect
	go.mau.fi/zeroconfig v0.1.2 // indirect
	golang.org/x/crypto v0.10.0 // indirect
	golang.org/x/sys v0.9.0 // indirect
	golang.org/x/text v0.10.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

// Exclude some things that cause go.sum to explode
exclude (
	cloud.google.com/go v0.65.0
	github.com/prometheus/client_golang v1.12.1
	google.golang.org/appengine v1.6.6
)
