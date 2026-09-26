package cli

import "testing"

func TestParseInput(t *testing.T) {
	tests := []struct {
		name string
		line string
		want parsedInput
	}{
		{
			name: "empty input",
			line: "",
			want: parsedInput{},
		},
		{
			name: "whitespace-only input",
			line: " \t\n ",
			want: parsedInput{},
		},
		{
			name: "ordinary text is trimmed",
			line: "  hello there  ",
			want: parsedInput{text: "hello there"},
		},
		{
			name: "ordinary text preserves internal spacing",
			line: " \thello  there\nfriend \t ",
			want: parsedInput{text: "hello  there\nfriend"},
		},
		{
			name: "help command",
			line: "/help",
			want: parsedInput{command: "help"},
		},
		{
			name: "command arguments remain a raw remainder",
			line: "/edit msg-id replacement text",
			want: parsedInput{command: "edit", args: "msg-id replacement text"},
		},
		{
			name: "command is lower-case and argument spacing is preserved",
			line: "  /EDIT   msg-id  replacement text  ",
			want: parsedInput{command: "edit", args: "msg-id  replacement text"},
		},
		{
			name: "bare slash is ignored",
			line: " / ",
			want: parsedInput{},
		},
		{
			name: "slash without a command token is ignored",
			line: "/ \t",
			want: parsedInput{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseInput(tt.line); got != tt.want {
				t.Errorf("parseInput(%q) = %+v, want %+v", tt.line, got, tt.want)
			}
		})
	}
}
