package channel

import "time"

const (
	Kind = "Channel"
)

type Channel struct {
	LastMessageAt time.Time
}
