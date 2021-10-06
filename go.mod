module maunium.net/go/mautrix-whatsapp

go 1.14

require (
	github.com/Rhymen/go-whatsapp v0.1.0
	github.com/gorilla/websocket v1.4.2
	github.com/lib/pq v1.10.2
	github.com/mattn/go-sqlite3 v1.14.8
	github.com/prometheus/client_golang v1.11.0
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	golang.org/x/image v0.0.0-20210628002857-a66eb6448b8d
	gopkg.in/yaml.v2 v2.4.0
	maunium.net/go/mauflag v1.0.0
	maunium.net/go/maulogger/v2 v2.3.0
	maunium.net/go/mautrix v0.9.27
)

replace github.com/Rhymen/go-whatsapp => github.com/tulir/go-whatsapp v0.5.12
