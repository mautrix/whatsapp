module maunium.net/go/mautrix-whatsapp

go 1.18

require (
	github.com/chai2010/webp v1.1.1
	github.com/gorilla/mux v1.8.0
	github.com/gorilla/websocket v1.5.0
	github.com/lib/pq v1.10.7
	github.com/mattn/go-sqlite3 v1.14.16
	github.com/prometheus/client_golang v1.14.0
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/tidwall/gjson v1.14.4
	go.mau.fi/whatsmeow v0.0.0-20230101112920-9b93048f5e21
	golang.org/x/image v0.2.0
	golang.org/x/net v0.4.0
	google.golang.org/protobuf v1.28.1
	maunium.net/go/maulogger/v2 v2.3.2
	maunium.net/go/mautrix v0.12.4
)

require (
	filippo.io/edwards25519 v1.0.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.1.2 // indirect
	github.com/golang/protobuf v1.5.2 // indirect
	github.com/mattn/go-colorable v0.1.12 // indirect
	github.com/mattn/go-isatty v0.0.14 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.1 // indirect
	github.com/prometheus/client_model v0.3.0 // indirect
	github.com/prometheus/common v0.37.0 // indirect
	github.com/prometheus/procfs v0.8.0 // indirect
	github.com/rs/zerolog v1.28.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/yuin/goldmark v1.5.3 // indirect
	go.mau.fi/libsignal v0.1.0 // indirect
	golang.org/x/crypto v0.4.0 // indirect
	golang.org/x/sys v0.3.0 // indirect
	golang.org/x/text v0.5.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

// Exclude some things that cause go.sum to explode
exclude (
	cloud.google.com/go v0.65.0
	github.com/prometheus/client_golang v1.12.1
	google.golang.org/appengine v1.6.6
)
