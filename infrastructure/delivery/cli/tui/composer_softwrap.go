package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/textarea"
	rw "github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

// composerDisplayRows is the number of viewport rows the textarea needs,
// including soft wraps within each logical line. LineCount() only counts
// explicit newlines.
func composerDisplayRows(ta textarea.Model) int {
	value := ta.Value()
	if value == "" {
		return 1
	}
	w := ta.Width()
	if w <= 0 {
		return max(1, ta.LineCount())
	}
	rows := 0
	for _, line := range strings.Split(value, "\n") {
		rows += len(softWrapRunes([]rune(line), w))
	}
	return max(1, rows)
}

// softWrapRunes matches github.com/charmbracelet/bubbles/textarea wrap so
// composer height tracks what textarea.View renders.
func softWrapRunes(runes []rune, width int) [][]rune {
	if width <= 0 {
		return [][]rune{runes}
	}
	if len(runes) == 0 {
		return [][]rune{{}}
	}

	var (
		lines  = [][]rune{{}}
		word   = []rune{}
		row    int
		spaces int
	)

	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}

		if spaces > 0 {
			if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces > width {
				row++
				lines = append(lines, []rune{})
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], softWrapSpaces(spaces)...)
				spaces = 0
				word = nil
			} else {
				lines[row] = append(lines[row], word...)
				lines[row] = append(lines[row], softWrapSpaces(spaces)...)
				spaces = 0
				word = nil
			}
		} else if len(word) > 0 {
			lastCharLen := rw.RuneWidth(word[len(word)-1])
			if uniseg.StringWidth(string(word))+lastCharLen > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}

	if uniseg.StringWidth(string(lines[row]))+uniseg.StringWidth(string(word))+spaces >= width {
		lines = append(lines, []rune{})
		lines[row+1] = append(lines[row+1], word...)
		spaces++
		lines[row+1] = append(lines[row+1], softWrapSpaces(spaces)...)
	} else {
		lines[row] = append(lines[row], word...)
		spaces++
		lines[row] = append(lines[row], softWrapSpaces(spaces)...)
	}

	return lines
}

func softWrapSpaces(n int) []rune {
	return []rune(strings.Repeat(" ", n))
}
