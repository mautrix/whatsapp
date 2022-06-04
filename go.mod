module maunium.net/go/mautrix-whatsapp

go 1.17

require (
	github.com/gorilla/mux v1.8.0
	github.com/gorilla/websocket v1.5.0
	github.com/lib/pq v1.10.6
	github.com/mattn/go-sqlite3 v1.14.13
	github.com/prometheus/client_golang v1.12.2-0.20220514081015-5d584e2717ef
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/tidwall/gjson v1.14.1
	go.mau.fi/whatsmeow v0.0.0-20220604174810-f28d984f1b9a
	golang.org/x/image v0.0.0-20220413100746-70e8d0d3baa9
	golang.org/x/net v0.0.0-20220513224357-95641704303c
	google.golang.org/protobuf v1.28.0
	maunium.net/go/maulogger/v2 v2.3.2
	maunium.net/go/mautrix v0.11.1-0.20220531155421-43e9bc0bdd59
)

require (
	filippo.io/edwards25519 v1.0.0-rc.1 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.1.2 // indirect
	github.com/golang/protobuf v1.5.2 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.1 // indirect
	github.com/prometheus/client_model v0.2.0 // indirect
	github.com/prometheus/common v0.34.0 // indirect
	github.com/prometheus/procfs v0.7.3 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/tidwall/sjson v1.2.4 // indirect
	github.com/yuin/goldmark v1.4.12 // indirect
	go.mau.fi/libsignal v0.0.0-20220425070825-c40c839ee6a0 // indirect
	golang.org/x/crypto v0.0.0-20220513210258-46612604a0f9 // indirect
	golang.org/x/sys v0.0.0-20220328115105-d36c6a25d886 // indirect
	golang.org/x/text v0.3.7 // indirect
	gopkg.in/yaml.v3 v3.0.0 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

// Exclude some things that cause go.sum to explode
exclude (
	cloud.google.com/go v0.65.0
	github.com/prometheus/client_golang v1.12.1
	google.golang.org/appengine v1.6.6
)
