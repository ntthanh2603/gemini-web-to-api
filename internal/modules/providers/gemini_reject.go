package providers

import "fmt"

// rejectCodeInvalidArgument is the status Gemini Web returns, in place of an
// answer, for a request it will never accept as sent. In practice this is a
// prompt longer than Gemini Web takes.
const rejectCodeInvalidArgument = 3

// RequestRejectedError reports a StreamGenerate reply that carries only a
// status code instead of an answer:
//
//	[["wrb.fr",null,null,null,null,[3]]]
type RequestRejectedError struct {
	Code int
}

func (e *RequestRejectedError) Error() string {
	if e.Code == rejectCodeInvalidArgument {
		return "Gemini Web rejected the request (status 3, invalid argument); this usually means the prompt is too long for Gemini Web, so shorten or compact the conversation"
	}
	return fmt.Sprintf("Gemini Web rejected the request (status %d) without an answer", e.Code)
}

// Retryable reports whether sending the same request again can succeed.
// Status 3 is returned for every attempt of the same request, so retrying it
// only adds latency.
func (e *RequestRejectedError) Retryable() bool {
	return e.Code != rejectCodeInvalidArgument
}

// IsInvalidRequest reports whether the caller has to change the request.
func (e *RequestRejectedError) IsInvalidRequest() bool {
	return e.Code == rejectCodeInvalidArgument
}

// extractRequestRejection returns the status of a wrb.fr frame that has no
// payload and a status array at index 5, or nil for any other frame.
func extractRequestRejection(item []interface{}) *RequestRejectedError {
	if len(item) < 6 {
		return nil
	}
	if tag, _ := item[0].(string); tag != "wrb.fr" || item[2] != nil {
		return nil
	}
	status, ok := item[5].([]interface{})
	if !ok || len(status) == 0 {
		return nil
	}
	code, ok := status[0].(float64)
	if !ok || code <= 0 || code != float64(int(code)) {
		return nil
	}
	return &RequestRejectedError{Code: int(code)}
}
