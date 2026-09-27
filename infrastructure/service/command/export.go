package command

import "github.com/jahrulnr/nusallm/infrastructure/domain/valueobject"

// AttachmentPath parses and validates an /attach workspace path.
func AttachmentPath(raw string) (string, error) {
	workspace, err := valueobject.ParseWorkspacePath(raw)
	if err != nil {
		return "", err
	}
	return workspace.Path(), nil
}
