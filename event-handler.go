package main

import (
	"maunium.net/go/mautrix"
	mevent "maunium.net/go/mautrix/event"
)

func HandleMessage(_ mautrix.EventSource, event *mevent.Event) {
}

func HandleReaction(_ mautrix.EventSource, event *mevent.Event) {
}

func HandleRedaction(_ mautrix.EventSource, event *mevent.Event) {
}
