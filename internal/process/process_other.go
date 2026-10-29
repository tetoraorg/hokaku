//go:build !linux

package process

import (
	"context"
	"time"
)

func read(context.Context) *Snapshot {
	return &Snapshot{At: time.Now().UnixMilli(), Status: "unavailable", Items: []Counter{}}
}
