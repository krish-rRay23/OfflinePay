package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type APIError struct {
	Code      string    `json:"code"`
	Message   string    `json:"message"`
	TraceID   string    `json:"trace_id"`
	Timestamp time.Time `json:"timestamp"`
}

// RespondWithError writes a standardized JSON error response to the client
func RespondWithError(c *gin.Context, httpStatus int, code string, message string) {
	traceID, exists := c.Get("trace_id")
	traceIDStr := ""
	if exists {
		if id, ok := traceID.(string); ok {
			traceIDStr = id
		}
	}

	// Fallback to internal server error if no status provided
	if httpStatus == 0 {
		httpStatus = http.StatusInternalServerError
	}

	errPayload := APIError{
		Code:      code,
		Message:   message,
		TraceID:   traceIDStr,
		Timestamp: time.Now().UTC(),
	}

	c.AbortWithStatusJSON(httpStatus, errPayload)
}
