#!/bin/sh

if [[ -z "$GID" ]]; then
	GID="$UID"
fi

# Define functions.
function fixperms {
	chown -R $UID:$GID /data

	# /opt/mautrix-whatsapp is read-only, so disable file logging if it's pointing there.
	if [[ "$(yq e '.logging.directory' /data/config.yaml)" == "./logs" ]]; then
		yq -I4 e -i '.logging.file_name_format = ""' /data/config.yaml
	fi
}

if [[ ! -f /data/config.yaml ]]; then
	cp /opt/mautrix-whatsapp/example-config.yaml /data/config.yaml
	echo "Didn't find a config file."
	echo "Copied default config file to /data/config.yaml"
	echo "Modify that config file to your liking."
	echo "Start the container again after that to generate the registration file."
	exit
fi

if [[ ! -f /data/registration.yaml ]]; then
	/usr/bin/mautrix-whatsapp -g -c /data/config.yaml -r /data/registration.yaml || exit $?
	echo "Didn't find a registration file."
	echo "Generated one for you."
	echo "See https://docs.mau.fi/bridges/general/registering-appservices.html on how to use it."
	exit
fi

cd /data
fixperms
exec su-exec $UID:$GID /usr/bin/mautrix-whatsapp
