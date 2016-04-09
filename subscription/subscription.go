package subscription

import "time"

const (
	Kind = "Subscription"
)

type Subscription struct {
	Channel string
	IID     string
	Timeout time.Time
}
