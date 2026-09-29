// Package transport holds the shared HTTP mechanics every boundary
// package uses: RFC 9457 problem rendering, strict body decoding, the
// paging/range parsers, and the path-id rule. The contract's error
// classification and Usage wire shape live in the delivery-level
// transport package so the WebSocket boundary shares one mapping.
// Boundary packages import this package only — never the router root
// or sibling boundaries.
package transport

import (
	"github.com/gin-gonic/gin"

	dtransport "github.com/jahrulnr/nusallm/infrastructure/delivery/transport"
)

// Problem is the contract's RFC 9457 body, shared with the WS delivery.
type Problem = dtransport.Problem

// WriteProblem renders the error and ends the handler chain.
func WriteProblem(c *gin.Context, status int, code, detail string) {
	c.Header("Content-Type", "application/problem+json")
	c.Abort()
	c.JSON(status, dtransport.NewProblem(status, code, detail))
}

// RespondError maps an application-layer error onto the contract's
// closed code set and renders the problem body.
func RespondError(c *gin.Context, err error) {
	status, code, detail := dtransport.ClassifyError(err)
	WriteProblem(c, status, code, detail)
}
