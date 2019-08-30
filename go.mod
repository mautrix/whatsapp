module maunium.net/go/mautrix-whatsapp

go 1.11

require (
	github.com/Rhymen/go-whatsapp v0.0.2-0.20190524185555-8d76e32a6d8e
	github.com/lib/pq v1.1.1
	github.com/mattn/go-sqlite3 v1.10.0
	github.com/pkg/errors v0.8.1
	github.com/skip2/go-qrcode v0.0.0-20190110000554-dc11ecdae0a9
	gopkg.in/yaml.v2 v2.2.2
	maunium.net/go/mauflag v1.0.0
	maunium.net/go/maulogger/v2 v2.0.0
	maunium.net/go/mautrix v0.1.0-alpha.3.0.20190825132810-9d870654e9d2
	maunium.net/go/mautrix-appservice v0.1.0-alpha.3.0.20190830063827-e7dcd7e42e7c
)

replace (
	github.com/Rhymen/go-whatsapp => github.com/tulir/go-whatsapp v0.0.2-0.20190830192205-ac9aa7e315cb
	gopkg.in/russross/blackfriday.v2 => github.com/russross/blackfriday/v2 v2.0.1
)
