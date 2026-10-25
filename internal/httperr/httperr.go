// Package httperr writes the uniform API error format (spec §11):
//
//	{"error": {"code": "agent_offline", "message": "…"}}
package httperr

import "github.com/gin-gonic/gin"

// Body is the uniform error envelope.
type Body struct {
	Error Detail `json:"error"`
}

// Detail carries a machine-readable code, a human message and optional details
// (e.g. the list of configuration problems).
type Detail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// Abort writes the error and stops the handler chain.
func Abort(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, Body{Error: Detail{Code: code, Message: message}})
}

// AbortWithDetails writes the error with structured details.
func AbortWithDetails(c *gin.Context, status int, code, message string, details any) {
	c.AbortWithStatusJSON(status, Body{Error: Detail{Code: code, Message: message, Details: details}})
}
