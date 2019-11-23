# mautrix-whatsapp
A Matrix-WhatsApp puppeting bridge based the [Rhymen/go-whatsapp](https://github.com/Rhymen/go-whatsapp)
implementation of the [sigalor/whatsapp-web-reveng](https://github.com/sigalor/whatsapp-web-reveng) project.

### Setting up the Bridge

See the [docs](https://github.com/tulir/mautrix-whatsapp/blob/master/docs/bridge_setup.md) for setting up the bridge.

See the [wiki](https://github.com/tulir/mautrix-whatsapp/wiki/Bridge-setup-with-Docker) for setting up the brdige with Docker.

### Connecting the brdige to WhatsApp

See the [docs](https://github.com/tulir/mautrix-whatsapp/blob/master/docs/authentication.md) for setting up the bridge.

### Setting up WhatsApp in an Android VM

Documentation in progress. See the original documentation on the [wiki](https://github.com/tulir/mautrix-whatsapp/wiki/Android-VM-Setup).

### The older wiki

The [Wiki](https://github.com/tulir/mautrix-whatsapp/wiki) contains all of the original documentation.

### [Features & Roadmap](https://github.com/tulir/mautrix-whatsapp/blob/master/ROADMAP.md)

## Discussion

Matrix room: [#whatsapp:maunium.net](https://matrix.to/#/#whatsapp:maunium.net)

In case you need to upload your logs somewhere, be aware that they contain your contacts' and your phone numbers. Strip them out with `| sed -r 's/[0-9]{10,}/📞/g'`
