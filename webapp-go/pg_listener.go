package isureal

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

type PGListener struct {
	dsn         string
	mu          sync.RWMutex
	byChannel   map[string][]func(payload string)
	allHandlers []func(payload string)
	channels    []string
	startOnce   sync.Once
}

func NewPGListener(dsn string) *PGListener {
	return &PGListener{
		dsn:       dsn,
		byChannel: make(map[string][]func(payload string)),
	}
}

func (l *PGListener) Handle(channel string, fn func(payload string)) {
	if fn == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, exists := l.byChannel[channel]; !exists {
		l.channels = append(l.channels, channel)
	}
	l.byChannel[channel] = append(l.byChannel[channel], fn)
	l.allHandlers = append(l.allHandlers, fn)
}

func (l *PGListener) Start() {
	l.startOnce.Do(func() {
		go l.run()
	})
}

func (l *PGListener) run() {
	const (
		minBackoff = 200 * time.Millisecond
		maxBackoff = 5 * time.Second
	)
	backoff := minBackoff

	for {
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, l.dsn)
		if err != nil {
			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		l.mu.RLock()
		channels := append([]string(nil), l.channels...)
		l.mu.RUnlock()

		listenErr := false
		for _, ch := range channels {
			sql := "LISTEN " + pgx.Identifier{ch}.Sanitize()
			if _, err := conn.Exec(ctx, sql); err != nil {
				listenErr = true
				break
			}
		}

		if listenErr {
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = conn.Close(closeCtx)
			cancel()

			time.Sleep(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		// Connected and re-LISTENed successfully
		backoff = minBackoff
		l.resyncAll()

		for {
			notification, err := conn.WaitForNotification(ctx)
			if err != nil {
				closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = conn.Close(closeCtx)
				cancel()

				time.Sleep(backoff)
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				break
			}

			if notification != nil {
				l.dispatch(notification.Channel, notification.Payload)
			}
		}
	}
}

func (l *PGListener) dispatch(channel string, payload string) {
	l.mu.RLock()
	var fns []func(payload string)
	if list := l.byChannel[channel]; len(list) > 0 {
		fns = append(fns, list...)
	}
	l.mu.RUnlock()

	for _, fn := range fns {
		safeInvoke(fn, payload)
	}
}

func (l *PGListener) resyncAll() {
	l.mu.RLock()
	var fns []func(payload string)
	if len(l.allHandlers) > 0 {
		fns = append(fns, l.allHandlers...)
	}
	l.mu.RUnlock()

	for _, fn := range fns {
		safeInvoke(fn, "")
	}
}

func safeInvoke(fn func(payload string), payload string) {
	if fn == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	fn(payload)
}
