module go.mau.fi/mautrix-whatsapp

go 1.23.0

toolchain go1.24.1

require (
	github.com/gorilla/mux v1.8.0
	github.com/gorilla/websocket v1.5.0
	github.com/lib/pq v1.10.9
	github.com/rs/zerolog v1.33.0
	go.mau.fi/util v0.8.6
	go.mau.fi/webp v0.2.0
	go.mau.fi/whatsmeow v0.0.0-20250326122510-493635f6f3f8
	golang.org/x/image v0.25.0
	golang.org/x/net v0.37.0
	golang.org/x/sync v0.12.0
	google.golang.org/protobuf v1.36.5
	gopkg.in/yaml.v3 v3.0.1
	maunium.net/go/mautrix v0.23.3-0.20250320134109-06f200da0d10
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-sqlite3 v1.14.24 // indirect
	github.com/petermattis/goid v0.0.0-20250303134427-723919f7f203 // indirect
	github.com/rogpeppe/go-internal v1.10.0 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/yuin/goldmark v1.7.8 // indirect
	go.mau.fi/libsignal v0.1.2 // indirect
	go.mau.fi/zeroconfig v0.1.3 // indirect
	golang.org/x/crypto v0.36.0 // indirect
	golang.org/x/exp v0.0.0-20250305212735-054e65f0b394 // indirect
	golang.org/x/sys v0.31.0 // indirect
	golang.org/x/text v0.23.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	maunium.net/go/mauflag v1.0.0 // indirect
)

// Remove this line to use the local version of golib
//replace maunium.net/go/mautrix => ./golib
