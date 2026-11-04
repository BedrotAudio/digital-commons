package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// maxLineBytes caps one SSE line. It is far above anything the gateway sends, and
// only stops a broken peer from exhausting memory.
const maxLineBytes = 16 << 20

const messageLost = "The connection was lost."

// StreamEvent is one item from StreamChat. Use a type switch over the *Event
// types below.
type StreamEvent interface{ isStreamEvent() }

// StartEvent opens a turn. Before it arrives nothing was stored, even when the
// caller closed the connection, so Try again resends. After it, Try again means
// regenerate.
type StartEvent struct {
	RequestID      string      `json:"request_id"`
	ConversationID string      `json:"conversation_id"`
	MessageID      string      `json:"message_id"`
	Title          string      `json:"title"`
	Route          Route       `json:"route"`
	Context        ContextInfo `json:"context"`
	Notices        []Notice    `json:"notices"`
}

// CitationsEvent carries the sources. It always precedes the first delta.
type CitationsEvent struct {
	Citations []Citation `json:"citations"`
}

// NoticesEvent replaces the notices in StartEvent, after the gateway had to retry
// with a shorter prompt. A CitationsEvent then replaces the first one too.
type NoticesEvent struct {
	Notices []Notice `json:"notices"`
}

// StatusEvent is shown verbatim. New phases are non-breaking.
type StatusEvent struct {
	Phase   string `json:"phase"`
	Message string `json:"message"`
}

// ThinkingEvent is reasoning text. Clean the accumulated text again before
// display, because a sequence can straddle two events.
type ThinkingEvent struct {
	Text string `json:"text"`
}

// DeltaEvent is reply text. Clean the accumulated text again before display,
// because a sequence can straddle two events.
type DeltaEvent struct {
	Text string `json:"text"`
}

// DoneEvent ends a turn successfully. FinishReason is stop or length.
type DoneEvent struct {
	MessageID    string `json:"message_id"`
	FinishReason string `json:"finish_reason"`
	Usage        Usage  `json:"usage"`
}

// ErrorEvent ends a turn with an error from the gateway, either a non-2xx answer
// to the POST or an error event in the stream. Err.Kind is KindAuth for a 401.
type ErrorEvent struct {
	Err *Error
}

// LostEvent ends a turn whose connection failed: no bytes for the idle timeout, a
// stream that ended without done or error, a refused redirect, or a stream that
// could not be understood. Keep the partial text and show Err.Message.
type LostEvent struct {
	Err *Error
}

func (StartEvent) isStreamEvent()     {}
func (CitationsEvent) isStreamEvent() {}
func (NoticesEvent) isStreamEvent()   {}
func (StatusEvent) isStreamEvent()    {}
func (ThinkingEvent) isStreamEvent()  {}
func (DeltaEvent) isStreamEvent()     {}
func (DoneEvent) isStreamEvent()      {}
func (ErrorEvent) isStreamEvent()     {}
func (LostEvent) isStreamEvent()      {}

// StreamChat sends one turn and returns immediately. Every outcome arrives on the
// channel, which ends with exactly one of DoneEvent, ErrorEvent or LostEvent and
// is then closed. Cancelling ctx closes the connection, which cancels generation
// on the gateway; the channel then closes with no final event. A caller that
// stops reading must cancel ctx.
func (c *Client) StreamChat(ctx context.Context, req ChatRequest) <-chan StreamEvent {
	out := make(chan StreamEvent, 16)
	go func() {
		defer close(out)
		c.runStream(ctx, req, out)
	}()
	return out
}

func (c *Client) runStream(parent context.Context, chat ChatRequest, out chan<- StreamEvent) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	var silent atomic.Bool
	expire := func() {
		silent.Store(true)
		cancel()
	}
	lost := func(cause error) {
		if parent.Err() != nil {
			return
		}
		if silent.Load() {
			cause = fmt.Errorf("no data for %s", c.idleTimeout)
		}
		send(parent, out, LostEvent{Err: lostError(cause)})
	}

	body, err := encodeChat(chat)
	if err != nil {
		lost(err)
		return
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/chat/stream", body, true)
	if err != nil {
		lost(err)
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Accept-Encoding", "identity")

	waiting := time.AfterFunc(c.idleTimeout, expire)
	resp, err := c.http.Do(req)
	waiting.Stop()
	if err != nil {
		lost(err)
		return
	}
	defer resp.Body.Close()

	if err := checkResponse(resp); err != nil {
		if IsNetwork(err) {
			lost(err)
		} else {
			send(parent, out, ErrorEvent{Err: asError(err)})
		}
		return
	}

	reader := bufio.NewReader(&idleReader{r: resp.Body, timeout: c.idleTimeout, expire: expire})
	if final, err := readEvents(parent, reader, out); !final {
		lost(err)
	}
}

func asError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return networkError(0, err)
}

func lostError(cause error) *Error {
	e := networkError(0, cause)
	e.Message = messageLost
	return e
}

func encodeChat(r ChatRequest) ([]byte, error) {
	if r.Attachments == nil {
		r.Attachments = []Attachment{}
	}
	if r.Tier == "" {
		r.Tier = "auto"
	}
	if r.Assistant == "" {
		r.Assistant = "general"
	}
	return json.Marshal(r)
}

// send delivers one event unless ctx ends first.
func send(ctx context.Context, out chan<- StreamEvent, ev StreamEvent) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// idleReader expires when one Read waits longer than timeout. The timer runs only
// while reading, so a slow consumer is not mistaken for a silent gateway.
type idleReader struct {
	r       io.Reader
	timeout time.Duration
	expire  func()
}

func (i *idleReader) Read(p []byte) (int, error) {
	t := time.AfterFunc(i.timeout, i.expire)
	defer t.Stop()
	return i.r.Read(p)
}

// readEvents parses the stream until a final event is delivered, which returns
// true. Otherwise it returns false and the reason reading stopped.
func readEvents(ctx context.Context, r *bufio.Reader, out chan<- StreamEvent) (bool, error) {
	var name string
	var data []byte
	for {
		line, err := readLine(r)
		if err != nil {
			return false, err
		}
		line = bytes.TrimRight(line, "\r\n")

		switch {
		case len(line) == 0:
			ev, final, err := parseEvent(name, data)
			name, data = "", nil
			if err != nil {
				return false, err
			}
			if ev != nil && !send(ctx, out, ev) {
				return false, ctx.Err()
			}
			if final {
				return true, nil
			}
		case line[0] == ':':
			// A comment, including the gateway's ping.
		default:
			field, value, _ := bytes.Cut(line, []byte{':'})
			value = bytes.TrimPrefix(value, []byte{' '})
			switch string(field) {
			case "event":
				name = string(value)
			case "data":
				if data != nil {
					data = append(data, '\n')
				}
				data = append(data, value...)
			}
		}
	}
}

// readLine reads through the next newline, of any length up to maxLineBytes.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxLineBytes {
			return nil, errors.New("stream line too long")
		}
		if err == nil {
			return line, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

// parseEvent decodes one event. An unknown name or an event with no data returns
// nil, so the gateway can add events without breaking older terminals. It also
// reports whether the event ends the turn.
func parseEvent(name string, data []byte) (StreamEvent, bool, error) {
	if data == nil {
		return nil, false, nil
	}
	switch name {
	case "start":
		return decodeEvent[StartEvent](name, data, false)
	case "citations":
		return decodeEvent[CitationsEvent](name, data, false)
	case "notices":
		return decodeEvent[NoticesEvent](name, data, false)
	case "status":
		return decodeEvent[StatusEvent](name, data, false)
	case "thinking":
		return decodeEvent[ThinkingEvent](name, data, false)
	case "delta":
		return decodeEvent[DeltaEvent](name, data, false)
	case "done":
		return decodeEvent[DoneEvent](name, data, true)
	case "error":
		var body errorBody
		if err := decode(data, &body); err != nil {
			return nil, false, fmt.Errorf("error event: %w", err)
		}
		return ErrorEvent{Err: apiError(0, &body)}, true, nil
	}
	return nil, false, nil
}

func decodeEvent[T StreamEvent](name string, data []byte, final bool) (StreamEvent, bool, error) {
	var v T
	if err := decode(data, &v); err != nil {
		return nil, false, fmt.Errorf("%s event: %w", name, err)
	}
	return v, final, nil
}
