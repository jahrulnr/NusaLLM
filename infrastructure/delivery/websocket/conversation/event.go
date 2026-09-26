package wsconversation

import "context"

type Event struct{}

func NewEvent() Event {
	return Event{}
}

func (d Event) Push(ctx context.Context) error {
	return nil
}

func (d Event) Receive(ctx context.Context) error {
	return nil
}
