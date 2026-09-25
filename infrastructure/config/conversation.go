package config

type ConversationConfig struct {
	MailboxCap  int `json:"mailbox_cap" default:"128"`
	MaxTurnStep int `json:"max_turn_steps" default:"16"`
}

// Normalize clamps invalid values back to unconfigured, then fills zeros
// from the `default` tags — one pass yields a struct every consumer may
// read raw. A negative cap is not "unlimited", it is a typo.
func (c *ConversationConfig) Normalize() {
	if c.MailboxCap < 0 {
		c.MailboxCap = 0
	}
	if c.MaxTurnStep < 0 {
		c.MaxTurnStep = 0
	}
	ApplyDefaults(c)
}
