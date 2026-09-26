package wsconversation

import "context"

type Delta struct{}

func NewDelta() Delta {
	return Delta{}
}

func (d Delta) Push(ctx context.Context) error {
	return nil
}
