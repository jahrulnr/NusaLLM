package wire

import (
	"strings"
	"testing"

	apperror "github.com/jahrulnr/nusallm/infrastructure/error"
)

func TestFailureMessageMatrix(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		msg, code string
		want      string
	}{
		{"provider message", 400, "invalid reasoning_effort for this model", "", "invalid reasoning_effort for this model"},
		{"code only", 400, "", "MissingSessionID", "provider rejected the request (MissingSessionID)"},
		{"neither", 400, "", "", "provider rejected the request (HTTP 400)"},
		{"neither other status", 502, "", "", "provider rejected the request (HTTP 502)"},
		{"whitespace-only message falls back to code", 429, " \n\t ", "rate_limit", "provider rejected the request (rate_limit)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FailureMessage(tc.status, tc.msg, tc.code); got != tc.want {
				t.Fatalf("FailureMessage = %q, want %q", got, tc.want)
			}
		})
	}
}

// A toast gets one line: newlines, CRs, and whitespace runs collapse to
// single spaces.
func TestFailureMessageCollapsesWhitespace(t *testing.T) {
	msg := "first line\n\n  second\tline \r\nthird   line"
	want := "first line second line third line"
	if got := FailureMessage(400, msg, ""); got != want {
		t.Fatalf("collapsed = %q, want %q", got, want)
	}
}

// A provider rant is capped for the toast — cut ends with an ellipsis and
// the whole string stays within the cap.
func TestFailureMessageTruncatesLongMessage(t *testing.T) {
	msg := strings.Repeat("word ", 100) // 500 chars
	got := FailureMessage(400, msg, "")
	if r := []rune(got); len(r) > failureMessageMax {
		t.Fatalf("len = %d runes, want <= %d", len(r), failureMessageMax)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated message must end with ellipsis: %q", got)
	}
	if !strings.HasPrefix(got, "word word") {
		t.Fatalf("truncated message lost the prefix: %q", got)
	}
	// Exactly at the cap is left alone.
	msg = strings.Repeat("x", failureMessageMax)
	if got := FailureMessage(400, msg, ""); got != msg {
		t.Fatalf("cap-length message must pass through, got %d runes", len([]rune(got)))
	}
}

// The context_length remap inspects the ORIGINAL provider text — a marker
// sitting past the truncation cap must still route 400 → 413.
func TestStatusErrorRemapSeesUntruncatedMessage(t *testing.T) {
	msg := strings.Repeat("padding ", 50) + "context_length exceeded"
	err := StatusError(400, msg, "", []byte(`{"error":{}}`))
	appErr := apperror.As(err)
	if appErr.StatusCode() != 413 {
		t.Fatalf("status = %d, want 413", appErr.StatusCode())
	}
	if appErr.Error() != "context length exceeded" {
		t.Fatalf("message = %q", appErr.Error())
	}
}

// StatusError surfaces the humanized message and parks the raw body in
// Cause — a message-less body still names the HTTP status.
func TestStatusErrorSurfacesFallbackAndKeepsBody(t *testing.T) {
	body := []byte(`{"model":"x"}`)
	err := StatusError(400, "", "", body)
	appErr := apperror.As(err)
	if appErr.StatusCode() != 400 {
		t.Fatalf("status = %d, want 400", appErr.StatusCode())
	}
	if got := appErr.Error(); got != "provider rejected the request (HTTP 400)" {
		t.Fatalf("message = %q", got)
	}
	if !appErr.IsUpstream() {
		t.Fatal("upstream flag lost")
	}
	if !strings.Contains(appErr.Caused(), `{"model":"x"}`) {
		t.Fatalf("cause lost the raw body: %s", appErr.Caused())
	}
}
