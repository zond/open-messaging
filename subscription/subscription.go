package subscription

import "google.golang.org/appengine/datastore"

const (
	Kind = "Subscription"
)

type Subscription struct {
	ChannelKey *datastore.Key
	IID        string
}
