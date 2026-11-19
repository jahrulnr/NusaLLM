package repoconversation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/nusallm/infrastructure/domain/aggregate"
	"github.com/jahrulnr/nusallm/infrastructure/domain/entity"
	"github.com/jahrulnr/nusallm/infrastructure/domain/enum"
)

// Conversation history is owner data — it can contain pasted secrets,
// credentials, and private work. It must not be world-readable just
// because the process umask is permissive; the gateway and secret stores
// already clamp their files and directories.
func TestConversationFilesArePrivate(t *testing.T) {
	root := t.TempDir()
	repo := NewConversationRepository(root)
	conv, _ := aggregate.NewConversation("room")
	if err := repo.Save(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	msg, _ := entity.NewMessage(enum.RoleUser, "private")
	if _, err := repo.AppendMessage(t.Context(), "room", msg); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "room")
	for _, path := range []string{dir, filepath.Join(dir, metaFile), filepath.Join(dir, activeFile)} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s is group/world accessible: mode %o", path, fi.Mode().Perm())
		}
	}
}
