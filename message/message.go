package message

import (
	"time"

	"google.golang.org/appengine/datastore"
)

const (
	Kind = "Message"
)

type Message struct {
	ChannelKey *datastore.Key
	CreatedAt  time.Time
	Payload    []byte
}
