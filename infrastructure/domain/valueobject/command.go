package valueobject

import (
	"errors"
	"strings"
	"unicode"
)

type Command struct {
	raw string
}

func NewCommand(raw string) Command {
	return Command{raw: strings.TrimSpace(raw)}
}

func (c Command) validate() error {
	if c.raw == "" {
		return errors.New("command is empty")
	}
	return nil
}

func (c Command) SplitFirst() (first, rest string, ok bool) {
	if c.validate() != nil {
		return "", "", false
	}
	boundary := strings.IndexFunc(c.raw, unicode.IsSpace)
	if boundary < 0 {
		return c.raw, "", true
	}
	return c.raw[:boundary], strings.TrimSpace(c.raw[boundary:]), true
}
