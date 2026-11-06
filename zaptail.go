// Package zaptail provides a zapcore.Core that batches log entries and ships
// them asynchronously to Better Stack Logtail over HTTP.
package zaptail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"go.uber.org/zap/zapcore"
)

const (
	defaultBatchSize     = 100
	defaultFlushInterval = 5 * time.Second
	defaultHTTPTimeout   = 10 * time.Second
	bufferMultiplier     = 10
	maxSendAttempts      = 3
	retryBackoff         = 200 * time.Millisecond
)

type Config struct {
	HTTPClient      *http.Client
	Context         context.Context
	ErrorOutput     zapcore.WriteSyncer
	APIKey          string
	LogtailURL      string
	BatchSize       int
	MaxBufferedLogs int
	FlushInterval   time.Duration
}

func (c *Config) validate() error {
	if c.APIKey == "" {
		return errors.New("zaptail: APIKey is required")
	}
	if c.LogtailURL == "" {
		return errors.New("zaptail: LogtailURL is required")
	}
	if parsed, err := url.Parse(c.LogtailURL); err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("zaptail: invalid LogtailURL %q", c.LogtailURL)
	}
	if c.BatchSize < 0 {
		return errors.New("zaptail: BatchSize must not be negative")
	}
	if c.MaxBufferedLogs < 0 {
		return errors.New("zaptail: MaxBufferedLogs must not be negative")
	}
	if c.FlushInterval < 0 {
		return errors.New("zaptail: FlushInterval must not be negative")
	}
	return nil
}

type sink struct {
	ctx       context.Context
	flushCh   chan struct{}
	stopCh    chan struct{}
	buffer    []map[string]any
	config    Config
	wg        sync.WaitGroup
	dropped   int
	bufferCap int
	closeOnce sync.Once
	mu        sync.Mutex
	reportMu  sync.Mutex
}

type Core struct {
	zapcore.LevelEnabler
	*sink
	encoder  zapcore.Encoder
	ownsSink bool
}

func NewCore(encoder zapcore.Encoder, enabler zapcore.LevelEnabler, config *Config) (*Core, error) {
	if encoder == nil {
		return nil, errors.New("zaptail: encoder is required")
	}
	if enabler == nil {
		return nil, errors.New("zaptail: level enabler is required")
	}
	if config == nil {
		return nil, errors.New("zaptail: config is required")
	}
	if err := config.validate(); err != nil {
		return nil, err
	}

	settings := *config
	if settings.BatchSize == 0 {
		settings.BatchSize = defaultBatchSize
	}
	if settings.FlushInterval == 0 {
		settings.FlushInterval = defaultFlushInterval
	}
	if settings.HTTPClient == nil {
		settings.HTTPClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	ctx := settings.Context
	if ctx == nil {
		ctx = context.Background()
	}

	shipped := &sink{
		config:    settings,
		ctx:       ctx,
		buffer:    make([]map[string]any, 0, settings.BatchSize),
		bufferCap: settings.MaxBufferedLogs,
		flushCh:   make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
	}
	if shipped.bufferCap == 0 {
		shipped.bufferCap = settings.BatchSize * bufferMultiplier
	}

	shipped.wg.Add(1)
	go shipped.worker()

	return &Core{LevelEnabler: enabler, sink: shipped, encoder: encoder, ownsSink: true}, nil
}

func (c *Core) With(fields []zapcore.Field) zapcore.Core {
	clone := &Core{
		LevelEnabler: c.LevelEnabler,
		sink:         c.sink,
		encoder:      c.encoder.Clone(),
	}
	for _, field := range fields {
		field.AddTo(clone.encoder)
	}
	return clone
}

//nolint:gocritic // Implements zapcore.Core interface
func (c *Core) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return ce.AddCore(entry, c)
	}
	return ce
}

//nolint:gocritic // Implements zapcore.Core interface
func (c *Core) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	buf, err := c.encoder.EncodeEntry(entry, fields)
	if err != nil {
		return err
	}
	defer buf.Free()

	var logEntry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		return err
	}
	if _, exists := logEntry["dt"]; !exists {
		logEntry["dt"] = entry.Time.Format(time.RFC3339Nano)
	}

	c.sink.append(logEntry)
	return nil
}

func (c *Core) Sync() error {
	return c.flush()
}

func (c *Core) Close() error {
	if c.ownsSink {
		c.closeOnce.Do(func() {
			close(c.stopCh)
			c.wg.Wait()
		})
	}
	return c.flush()
}

func (s *sink) worker() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.config.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_ = s.flush()
		case <-s.flushCh:
			_ = s.flush()
		case <-s.stopCh:
			return
		}
	}
}

func (s *sink) append(entry map[string]any) {
	s.mu.Lock()
	if len(s.buffer) >= s.bufferCap {
		s.dropped++
		s.mu.Unlock()
		return
	}
	s.buffer = append(s.buffer, entry)
	full := len(s.buffer) >= s.config.BatchSize
	s.mu.Unlock()

	if full {
		s.signalFlush()
	}
}

func (s *sink) signalFlush() {
	select {
	case s.flushCh <- struct{}{}:
	default:
	}
}

func (s *sink) flush() error {
	s.mu.Lock()
	batch := s.buffer
	s.buffer = make([]map[string]any, 0, s.config.BatchSize)
	dropped := s.dropped
	s.dropped = 0
	s.mu.Unlock()

	if dropped > 0 {
		s.report(fmt.Sprintf("log buffer full; dropped %d logs", dropped))
	}
	if len(batch) == 0 {
		return nil
	}

	if err := s.sendWithRetry(batch); err != nil {
		s.report(fmt.Sprintf("dropping %d logs: %v", len(batch), err))
		return err
	}
	return nil
}

func (s *sink) sendWithRetry(batch []map[string]any) error {
	var lastErr error
	attempts := 0
	for attempts < maxSendAttempts {
		if err := s.ctx.Err(); err != nil {
			return fmt.Errorf("delivery cancelled: %w", err)
		}
		lastErr = s.send(batch)
		if lastErr == nil {
			return nil
		}
		attempts++
		if attempts < maxSendAttempts && !s.wait(retryBackoff<<(attempts-1)) {
			break
		}
	}
	return fmt.Errorf("send failed after %d attempts: %w", attempts, lastErr)
}

func (s *sink) wait(delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-s.stopCh:
		return false
	case <-s.ctx.Done():
		return false
	}
}

func (s *sink) send(batch []map[string]any) error {
	payload, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("marshal logs: %w", err)
	}

	req, err := http.NewRequestWithContext(s.ctx, http.MethodPost, s.config.LogtailURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.config.APIKey)

	resp, err := s.config.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("send logs: %w", err)
	}
	defer resp.Body.Close()

	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("logtail returned status %d", resp.StatusCode)
	}
	return nil
}

func (s *sink) report(message string) {
	if s.config.ErrorOutput == nil {
		return
	}
	s.reportMu.Lock()
	defer s.reportMu.Unlock()
	_, _ = fmt.Fprintf(s.config.ErrorOutput, "zaptail: %s\n", message)
}
