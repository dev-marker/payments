package risk

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

//go:embed velocity.lua
var velocityLua string

var velocityScript = redis.NewScript(velocityLua)

// Recent is a user's recent transfers.
type Recent struct {
	Count  int   // in the count window
	Amount int64 // in the amount window, in US cents
}

// Velocity keeps each user's recent transfers in Redis, so every instance of
// the transfer service sees the same numbers.
type Velocity struct {
	rdb                       *redis.Client
	countWindow, amountWindow time.Duration
}

// NewVelocity returns a Velocity that counts transfers in countWindow, and
// adds up their amounts in amountWindow, the longer one.
func NewVelocity(rdb *redis.Client, countWindow, amountWindow time.Duration) *Velocity {
	return &Velocity{rdb: rdb, countWindow: countWindow, amountWindow: amountWindow}
}

// Record adds transfer id, of amount, to user's recent transfers, once, and
// returns them, this one included.
func (v *Velocity) Record(ctx context.Context, user, id uuid.UUID, amount int64) (Recent, error) {
	key := "risk:velocity:" + user.String()
	member := fmt.Sprintf("%s:%d", id, amount)
	res, err := velocityScript.Run(ctx, v.rdb, []string{key}, member,
		v.countWindow.Milliseconds(), v.amountWindow.Milliseconds()).Int64Slice()
	if err != nil {
		return Recent{}, fmt.Errorf("velocity of %s: %w", user, err)
	}
	return Recent{Count: int(res[0]), Amount: res[1]}, nil
}
