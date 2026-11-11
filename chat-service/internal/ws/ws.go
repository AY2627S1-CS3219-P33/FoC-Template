// AI Assistance Disclosure:
// Tool: Claude Code (model: Claude Opus 5.5), date: 2026-11-10
// Scope: Implementation of code.
// Author review: Reviewed, validated, and tested by the authors.

// Package ws runs chat-service's WebSocket endpoint, GET /ws: one connection
// per browser tab, from the handshake to the close. The frames are in
// package protocol and described for client authors in
// docs/websocket-protocol.md.
//
// A connection goes through these steps:
//
//  1. Accept, but only from an allowed page origin (WS_ALLOWED_ORIGINS).
//  2. Authenticate: the first frame must be {"type":"auth","token":...}
//     within WS_AUTH_TIMEOUT. Browsers cannot put an Authorization header on a
//     WebSocket, and tokens must not go in URLs, which get logged.
//  3. Register with the hub (at most WS_MAX_CONNECTIONS_PER_USER per user).
//  4. Send {"type":"ready"}; then three loops run until the connection ends:
//     reading (sends from the browser), writing (frames for this user from
//     the hub) and a heartbeat ping every WS_PING_INTERVAL.
//  5. Close when the token expires (4401), on shutdown (1012), when the client
//     is too slow (1008), or when the browser leaves.
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"golang.org/x/time/rate"

	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/apperror"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/auth"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/chat"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/hub"
	"github.com/CS3219-AY2627S1/FoC-Template/chat-service/internal/protocol"
)

// maxFrameBytes bounds a frame from the browser: a 2000-character message is
// at most 8000 bytes of UTF-8, and an access token about 1–2 KiB.
const maxFrameBytes = 16 * 1024

// Sender stores a message; *chat.Service implements it.
type Sender interface {
	Send(ctx context.Context, userID string, roomID uuid.UUID, clientMsgID, body string) (chat.MessageView, bool, error)
}

// Config holds the WebSocket settings.
type Config struct {
	// AllowedOrigins are host patterns of pages allowed to connect, for
	// example "app.bestfoc.app" or "localhost:5173". Requests without an
	// Origin header (non-browser tools) are allowed; they still need a token.
	AllowedOrigins []string
	AuthTimeout    time.Duration
	PingInterval   time.Duration
	// SendRate (messages per second) and SendBurst size each connection's
	// token bucket.
	SendRate  float64
	SendBurst int
	// WriteTimeout bounds one frame write; zero means 10 s.
	WriteTimeout time.Duration
	// Buffer is how many frames may wait for a slow client; zero means
	// hub.DefaultBuffer.
	Buffer int
}

// Handler serves GET /ws.
type Handler struct {
	verifier auth.Port
	sender   Sender
	hub      *hub.Hub
	cfg      Config
	logger   *slog.Logger

	mu       sync.Mutex
	closing  bool
	sessions sync.WaitGroup
	shutdown chan struct{}
}

func NewHandler(verifier auth.Port, sender Sender, connections *hub.Hub, cfg Config, logger *slog.Logger) *Handler {
	if verifier == nil || sender == nil || connections == nil || logger == nil {
		panic("ws: verifier, sender, hub and logger are required")
	}
	if cfg.AuthTimeout <= 0 || cfg.PingInterval <= 0 || cfg.SendRate <= 0 || cfg.SendBurst < 1 {
		panic("ws: timeouts, send rate and burst must be positive")
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = 10 * time.Second
	}
	if cfg.Buffer == 0 {
		cfg.Buffer = hub.DefaultBuffer
	}
	return &Handler{verifier: verifier, sender: sender, hub: connections, cfg: cfg, logger: logger, shutdown: make(chan struct{})}
}

func (h *Handler) RegisterRoutes(router *http.ServeMux) {
	router.HandleFunc("GET /ws", h.serve)
}

// Shutdown closes every connection with 1012 ("service restart"), so
// browsers reconnect at once (to another copy, once there are several), and
// waits for them to finish until ctx ends. New connections are refused.
// http.Server.Shutdown does not do this: it never waits for upgraded
// connections.
func (h *Handler) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	if !h.closing {
		h.closing = true
		close(h.shutdown)
	}
	h.mu.Unlock()
	done := make(chan struct{})
	go func() {
		h.sessions.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.New("ws: connections did not close in time")
	}
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		http.Error(w, "service is restarting", http.StatusServiceUnavailable)
		return
	}
	h.sessions.Add(1)
	h.mu.Unlock()
	defer h.sessions.Done()

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.cfg.AllowedOrigins})
	if err != nil {
		// Accept has answered (403 for a foreign origin, 400 for a bad handshake).
		h.logger.Debug("websocket handshake refused", "error", err.Error())
		return
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(maxFrameBytes)
	(&session{h: h, conn: conn}).run()
}

// session is one connection after the handshake.
type session struct {
	h    *Handler
	conn *websocket.Conn
}

func (s *session) run() {
	principal, ok := s.authenticate()
	if !ok {
		return
	}
	userID := principal.Subject
	client := hub.NewClient(userID, s.h.cfg.Buffer, func() {
		_ = s.conn.Close(protocol.ClosePolicyViolation, "too slow to keep up; reconnect")
	})
	if !s.h.hub.Register(client) {
		_ = s.conn.Close(protocol.CloseTooManyConns, "too many open connections")
		return
	}
	defer s.h.hub.Unregister(client)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.write(ctx, protocol.Encode(protocol.Ready{Type: protocol.TypeReady, UserID: userID})); err != nil {
		return
	}

	ended := make(chan error, 3)
	go func() { ended <- s.readLoop(ctx, userID) }()
	go func() { ended <- s.writeLoop(ctx, client) }()
	go func() { ended <- s.heartbeat(ctx) }()

	var expired <-chan time.Time
	if !principal.ExpiresAt.IsZero() {
		timer := time.NewTimer(time.Until(principal.ExpiresAt))
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case <-ended:
		// The browser left, the network failed, or a loop closed the
		// connection itself (bad frame, missed pong).
	case <-expired:
		_ = s.conn.Close(protocol.CloseUnauthenticated, "token expired; reconnect with a new token")
	case <-s.h.shutdown:
		_ = s.conn.Close(protocol.CloseServiceRestart, "service restart; reconnect")
	}
}

// authenticate reads the auth frame. On failure it closes the connection
// with a code that tells the browser what to do, and returns false.
func (s *session) authenticate() (auth.Principal, bool) {
	type result struct {
		data []byte
		err  error
	}
	first := make(chan result, 1)
	go func() {
		// Not bounded by a context: cancelling one closes the connection with
		// 1008, and an auth timeout must close it with 4401. Closing the
		// connection below ends this read.
		typ, data, err := s.conn.Read(context.Background())
		if err == nil && typ != websocket.MessageText {
			err = errors.New("binary frame")
		}
		first <- result{data, err}
	}()

	timer := time.NewTimer(s.h.cfg.AuthTimeout)
	defer timer.Stop()
	var res result
	select {
	case <-timer.C:
		return s.refuse(protocol.CloseUnauthenticated, "no auth frame in time")
	case <-s.h.shutdown:
		return s.refuse(protocol.CloseServiceRestart, "service restart; reconnect")
	case res = <-first:
	}
	if res.err != nil {
		return auth.Principal{}, false // the browser left
	}
	var frame protocol.ClientFrame
	if json.Unmarshal(res.data, &frame) != nil || frame.Type != protocol.TypeAuth || frame.Token == "" {
		return s.refuse(protocol.CloseUnauthenticated, `the first frame must be {"type":"auth","token":"..."}`)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	principal, err := s.h.verifier.Authenticate(ctx, frame.Token)
	if err != nil {
		var failure *auth.AuthenticationError
		switch {
		case errors.As(err, &failure) && failure.Kind == auth.VerifierUnavailable:
			return s.refuse(protocol.CloseTryAgainLater, "authentication service unavailable; reconnect later")
		case errors.As(err, &failure) && failure.Kind == auth.AccountDisabled:
			return s.refuse(protocol.CloseForbidden, "account is disabled")
		default:
			return s.refuse(protocol.CloseUnauthenticated, "invalid or expired token")
		}
	}
	if !principal.IsUser() || !principal.Has(auth.UseChats) {
		return s.refuse(protocol.CloseForbidden, "a user token with chats:use is required")
	}
	return principal, true
}

func (s *session) refuse(code websocket.StatusCode, reason string) (auth.Principal, bool) {
	_ = s.conn.Close(code, reason)
	return auth.Principal{}, false
}

// readLoop handles frames from the browser until the connection ends.
func (s *session) readLoop(ctx context.Context, userID string) error {
	limiter := rate.NewLimiter(rate.Limit(s.h.cfg.SendRate), s.h.cfg.SendBurst)
	for {
		typ, data, err := s.conn.Read(ctx)
		if err != nil {
			return err
		}
		var frame protocol.ClientFrame
		if typ != websocket.MessageText || json.Unmarshal(data, &frame) != nil {
			_ = s.conn.Close(protocol.ClosePolicyViolation, "frames must be JSON text")
			return errors.New("malformed frame")
		}
		switch frame.Type {
		case protocol.TypeSend:
			s.send(ctx, userID, frame, limiter)
		case protocol.TypeAuth:
			s.reject(ctx, apperror.InvalidArgument, "already authenticated", "")
		default:
			s.reject(ctx, apperror.InvalidArgument, fmt.Sprintf("unknown frame type %q", truncate(frame.Type, 32)), "")
		}
	}
}

// send stores a message and answers with ack or error. The message frame for
// both participants (including this tab) comes separately, through the hub.
func (s *session) send(ctx context.Context, userID string, frame protocol.ClientFrame, limiter *rate.Limiter) {
	if !limiter.Allow() {
		s.reject(ctx, apperror.RateLimited, "too many messages; slow down", frame.ClientMsgID)
		return
	}
	roomID, err := uuid.Parse(frame.RoomID)
	if err != nil {
		s.reject(ctx, apperror.InvalidArgument, "roomId must be a UUID", frame.ClientMsgID)
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	message, _, err := s.h.sender.Send(sendCtx, userID, roomID, frame.ClientMsgID, frame.Body)
	if err != nil {
		code, text, serverFault := apperror.FromChat(err)
		if serverFault {
			s.h.logger.WarnContext(ctx, "websocket send failed", "code", string(code), "error", err.Error())
		}
		s.reject(ctx, code, text, frame.ClientMsgID)
		return
	}
	_ = s.write(ctx, protocol.Encode(protocol.Ack{
		Type: protocol.TypeAck, ClientMsgID: frame.ClientMsgID, Message: protocol.FromMessageView(message),
	}))
}

func (s *session) reject(ctx context.Context, code apperror.Code, message, clientMsgID string) {
	_ = s.write(ctx, protocol.Encode(protocol.Error{
		Type: protocol.TypeError, Code: code, Message: message, ClientMsgID: clientMsgID,
	}))
}

// writeLoop sends the frames the hub queued for this connection.
func (s *session) writeLoop(ctx context.Context, client *hub.Client) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case payload := <-client.Outbox():
			if err := s.write(ctx, payload); err != nil {
				return err
			}
		}
	}
}

// heartbeat pings the browser. The ALB closes connections that are idle for
// 60 s; a ping every 25 s keeps them busy, and a missing pong reveals a dead
// connection.
func (s *session) heartbeat(ctx context.Context) error {
	ticker := time.NewTicker(s.h.cfg.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, s.h.cfg.PingInterval)
			err := s.conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}

func (s *session) write(ctx context.Context, payload []byte) error {
	writeCtx, cancel := context.WithTimeout(ctx, s.h.cfg.WriteTimeout)
	defer cancel()
	return s.conn.Write(writeCtx, websocket.MessageText, payload)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
