# Bridge setup

## Requirements

 - go 1.12+
 - A Matrix homeserver that supports application services (e.g. [Synapse](https://github.com/matrix-org/synapse).)
 - A WhatsApp client running on a phone or in an emulated Android VM.

## Setup

 1. Clone the repo with `git clone https://github.com/tulir/mautrix-whatsapp.git`
 2. Run `go build` to fetch dependencies and compile.
     - If your distro doesn't support go 1.12+ you can download the latest version from: https://golang.org/dl/
     - You will then need to export the path using something like this:
```
		export GOROOT=$HOME/mautrix-whatsapp/go
		export GOPATH=$HOME/mautrix-whatsapp/go/work
		export PATH=$GOROOT/bin:$GOPATH/bin:$PATH
```
 3. Copy example-config.yaml to config.yaml
 4. Update the config to your liking.
    - You need to make sure that the `address` and `domain` field point to your homeserver.
    - You will also need to add your user of admin user under the `permissions` section.
 5. Generate the appservice registration file by running `./mautrix-whatsapp -g`.
     - You can use the -c and -r flags to change the location of the config and registration files. They default to config.yaml and registration.yaml respectively.
 6. Add the path to the registration file to your synapse `homeserver.yaml` under `app_service_config_files`. You will then need to restart the synapse server. Remember to restart it everytime the registration file is regenerated.
 7. Run the bridge with ./mautrix-whatsapp.

## Updating

 1. Pull changes with `git pull`
 2. Recompile with `go build`
 3. Start the bridge again
