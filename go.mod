module maunium.net/go/mautrix-whatsapp

go 1.14

require (
	github.com/Rhymen/go-whatsapp v0.0.2-0.20190524185555-8d76e32a6d8e
	github.com/chai2010/webp v1.1.0
	github.com/gorilla/mux v1.7.2
	github.com/gorilla/websocket v1.4.1
	github.com/lib/pq v1.1.1
	github.com/mattn/go-sqlite3 v1.10.0
	github.com/pkg/errors v0.8.1
	github.com/shurcooL/sanitized_anchor_name v1.0.0 // indirect
	github.com/skip2/go-qrcode v0.0.0-20190110000554-dc11ecdae0a9
	gopkg.in/yaml.v2 v2.2.2
	maunium.net/go/mauflag v1.0.0
	maunium.net/go/maulogger/v2 v2.0.0
	maunium.net/go/mautrix v0.1.0-beta.1
	maunium.net/go/mautrix-appservice v0.1.0-alpha.5
)

replace github.com/Rhymen/go-whatsapp => github.com/tulir/go-whatsapp v0.0.2-0.20200326181432-0904cc134550
