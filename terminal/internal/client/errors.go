package client

import (
	"errors"
	"fmt"
)

// Kind separates the failures the UI treats differently.
type Kind int

const (
	// KindAPI is an error the gateway returned, or sent as an error event.
	KindAPI Kind = iota
	// KindNetwork means no usable answer arrived: refused, reset, timed out, a
	// redirect, or a body that is not the gateway's.
	KindNetwork
	// KindAuth is HTTP 401.
	KindAuth
)

// Error is every failure this package returns. Code, Message, Detail and
// Retryable come from the gateway's error body. Basic mode shows only Message.
type Error struct {
	Kind      Kind
	Status    int // HTTP status, 0 when none arrived
	Code      string
	Message   string
	Detail    string // technical, empty unless the caller holds view_diagnostics
	Retryable bool
	RequestID string
	Err       error // underlying cause of a network failure
}

func (e *Error) Error() string {
	switch {
	case e.Err != nil:
		return fmt.Sprintf("%s (%v)", e.Message, e.Err)
	case e.Code != "":
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// IsNetwork reports whether err is a connection failure, including a refused
// redirect.
func IsNetwork(err error) bool { return kindIs(err, KindNetwork) }

// IsAuth reports whether err is a 401 from the gateway.
func IsAuth(err error) bool { return kindIs(err, KindAuth) }

// IsAPI reports whether err is any other error returned by the gateway.
func IsAPI(err error) bool { return kindIs(err, KindAPI) }

func kindIs(err error, k Kind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == k
}

// Plain-language messages for failures that have no gateway message.
const (
	messageNetwork = "The service can't be reached."
	messageGeneric = "Something went wrong. Please try again."
)

func networkError(status int, cause error) *Error {
	return &Error{Kind: KindNetwork, Status: status, Code: "connection_failed", Message: messageNetwork, Retryable: true, Err: cause}
}
