package config

type ConversationConfig struct {
	MailboxCap  int `json:"mailbox_cap" default:"128"`
	MaxTurnStep int `json:"max_turn_steps" default:"16"`
	// CompactBudgetPct is the share of the model's context window that
	// triggers compaction — headroom above it stays reserved for the reply.
	CompactBudgetPct int `json:"compact_budget_pct" default:"90"`
	// CompactKeepTail is how many recent messages survive a compaction
	// alongside the checkpoint.
	CompactKeepTail int `json:"compact_keep_tail" default:"4"`
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
	if c.CompactBudgetPct < 0 || c.CompactBudgetPct > 100 {
		c.CompactBudgetPct = 0
	}
	if c.CompactKeepTail < 1 {
		c.CompactKeepTail = 0
	}
	ApplyDefaults(c)
}
