package tui

import (
	"strings"
	"unicode"
)

type parsedInput struct {
	command string
	args    string
	text    string
}

// parseInput separates ordinary chat text from a slash command and its raw
// remainder. A bare slash, including one followed only by whitespace, returns
// the zero value because it has no named command.
func parseInput(line string) parsedInput {
	line = strings.TrimSpace(line)
	if line == "" {
		return parsedInput{}
	}
	if line[0] != '/' {
		return parsedInput{text: line}
	}

	remainder := line[1:]
	if strings.HasPrefix(remainder, "/") {
		// "//..." is literal text — a leading double-slash is the escape,
		// not a command with a slash in its name.
		return parsedInput{text: line}
	}
	commandEnd := strings.IndexFunc(remainder, unicode.IsSpace)
	command := remainder
	args := ""
	if commandEnd >= 0 {
		command = remainder[:commandEnd]
		args = strings.TrimSpace(remainder[commandEnd:])
	}
	if command == "" {
		return parsedInput{}
	}

	return parsedInput{
		command: strings.ToLower(command),
		args:    args,
	}
}
