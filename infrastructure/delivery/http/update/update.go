// Package update is the WebUI update boundary: the cached "newest release"
// read the browser polls, the on-demand re-check, and the install job the
// browser starts and watches. A failed lookup or a failed install is data
// (the error field), not an HTTP failure, except where the request itself
// cannot run (busy, disabled, nothing newer).
package update

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	appupdate "github.com/jahrulnr/nusallm/application/update"
	"github.com/jahrulnr/nusallm/infrastructure/delivery/http/internal/transport"
)

// Checker is the update surface. *appupdate.App satisfies it.
type Checker interface {
	Status(ctx context.Context) appupdate.Info
	Check(ctx context.Context) appupdate.Info
	Install() (appupdate.Progress, error)
	Progress() appupdate.Progress
}

// Deps wires the use case; a nil Checker leaves the routes unregistered.
type Deps struct {
	Checks Checker
}

// Register mounts this boundary's routes under /api.
func Register(api *gin.RouterGroup, d Deps) {
	if d.Checks == nil {
		return
	}
	s := &server{checks: d.Checks}
	api.GET("/update", s.status)
	api.POST("/update/check", s.check)
	api.POST("/update/install", s.install)
	api.GET("/update/install", s.progress)
}

type server struct{ checks Checker }

// infoDTO is the browser-facing update picture. Timestamps are null until
// they exist, so "never checked" and "epoch" never look alike.
type infoDTO struct {
	Current        string     `json:"current"`
	Enabled        bool       `json:"enabled"`
	Installable    bool       `json:"installable"`
	Latest         string     `json:"latest"`
	Available      bool       `json:"available"`
	PublishedAt    *time.Time `json:"publishedAt"`
	ReleaseURL     string     `json:"releaseUrl"`
	Notes          string     `json:"notes"`
	InstallCommand string     `json:"installCommand"`
	CheckedAt      *time.Time `json:"checkedAt"`
	Error          string     `json:"error"`
}

// progressDTO is one install's state. Phase and restart are empty until the
// install reaches the step that sets them.
type progressDTO struct {
	State      string `json:"state"`
	Phase      string `json:"phase"`
	Version    string `json:"version"`
	BytesDone  int64  `json:"bytesDone"`
	BytesTotal int64  `json:"bytesTotal"`
	Restart    string `json:"restart"`
	Error      string `json:"error"`
}

func project(info appupdate.Info) infoDTO {
	dto := infoDTO{
		Current:        info.Current,
		Enabled:        info.Enabled,
		Installable:    info.Installable,
		Latest:         info.Latest,
		Available:      info.Available,
		ReleaseURL:     info.URL,
		Notes:          info.Notes,
		InstallCommand: info.InstallCommand,
		Error:          info.Error,
	}
	if !info.PublishedAt.IsZero() {
		at := info.PublishedAt
		dto.PublishedAt = &at
	}
	if !info.CheckedAt.IsZero() {
		at := info.CheckedAt
		dto.CheckedAt = &at
	}
	return dto
}

func projectProgress(p appupdate.Progress) progressDTO {
	return progressDTO{
		State:      string(p.State),
		Phase:      string(p.Phase),
		Version:    p.Version,
		BytesDone:  p.BytesDone,
		BytesTotal: p.BytesTotal,
		Restart:    p.Restart,
		Error:      p.Error,
	}
}

func (s *server) status(c *gin.Context) {
	c.JSON(http.StatusOK, project(s.checks.Status(c.Request.Context())))
}

func (s *server) check(c *gin.Context) {
	c.JSON(http.StatusOK, project(s.checks.Check(c.Request.Context())))
}

func (s *server) progress(c *gin.Context) {
	c.JSON(http.StatusOK, projectProgress(s.checks.Progress()))
}

// install starts the job and answers 202 with its first state. A refusal names
// its reason in the problem code, so the browser can say what to do next.
func (s *server) install(c *gin.Context) {
	progress, err := s.checks.Install()
	if err != nil {
		status, code, detail := installProblem(err)
		transport.WriteProblem(c, status, code, detail)
		return
	}
	c.JSON(http.StatusAccepted, projectProgress(progress))
}

func installProblem(err error) (status int, code, detail string) {
	switch {
	case errors.Is(err, appupdate.ErrBusy):
		return http.StatusConflict, "update_busy", err.Error()
	case errors.Is(err, appupdate.ErrDisabled):
		return http.StatusConflict, "update_disabled", err.Error()
	case errors.Is(err, appupdate.ErrNoUpdate):
		return http.StatusConflict, "update_not_available", err.Error()
	case errors.Is(err, appupdate.ErrNoRelease):
		return http.StatusConflict, "update_unknown", err.Error()
	default:
		return http.StatusInternalServerError, "update_failed", err.Error()
	}
}
