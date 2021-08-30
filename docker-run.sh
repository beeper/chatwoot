#!/bin/sh

if [[ -z "$GID" ]]; then
	GID="$UID"
fi

# Define functions.
function fixperms {
	chown -R $UID:$GID /data /opt/chatwoot
}

cd /data
fixperms
exec su-exec $UID:$GID /usr/bin/chatwoot
