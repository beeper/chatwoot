#!/bin/sh

if [[ -z "$GID" ]]; then
	GID="$UID"
fi

# Define functions.
function fixperms {
	chown -R $UID:$GID /data /opt/chatwoot
}

if [[ ! -f /data/config.yaml ]]; then
	cp /opt/chatwoot/config.sample.yaml /data/config.yaml
	echo "Didn't find a config file."
	echo "Copied default config file to /data/config.yaml"
	echo "Modify that config file to your liking."
	echo "Start the container again after that to generate the registration file."
	exit
fi

cd /data
fixperms
exec su-exec $UID:$GID /usr/bin/chatwoot
