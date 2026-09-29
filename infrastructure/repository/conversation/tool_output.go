package repoconversation

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	drepo "github.com/jahrulnr/nusallm/infrastructure/domain/repository"
)

type ToolOutputRepository struct{ root string }

func NewToolOutputRepository(root string) (drepo.ToolOutput, error) {
	if root == "" {
		return nil, fmt.Errorf("tool output root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("tool output root: %w", err)
	}
	return &ToolOutputRepository{root: abs}, nil
}

type toolOutputSpill struct{ path string }

func (s toolOutputSpill) Reference() string { return s.path }

func (s toolOutputSpill) Open() (io.WriteCloser, error) {
	return os.OpenFile(s.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
}

func (r *ToolOutputRepository) Reserve(ctx context.Context, conversationID, callID string) (drepo.ToolOutputSpill, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validID(conversationID); err != nil {
		return nil, err
	}
	convDir := filepath.Join(r.root, conversationID)
	info, err := os.Stat(convDir)
	if err != nil {
		return nil, fmt.Errorf("exec conversation %s unavailable: %w", conversationID, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("exec conversation %s is not a directory", conversationID)
	}
	spillDir := filepath.Join(convDir, "tool-output")
	if err := os.Mkdir(spillDir, 0o755); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("exec output dir: %w", err)
	}
	safe := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, callID)
	path := filepath.Join(spillDir, fmt.Sprintf("nusallm-exec-%s-%s.log", rand.Text(), safe))
	return toolOutputSpill{path: path}, nil
}
