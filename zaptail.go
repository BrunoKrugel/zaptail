package zaptail

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap/zapcore"
)

const (
	logtailURL = "https://in.logtail.com"
)

type Config struct {
	APIKey     string
	BatchSize  int
	FlushInterval time.Duration
	HTTPClient *http.Client
}

type Core struct {
	zapcore.LevelEnabler
	encoder zapcore.Encoder
	config  Config
	buffer  []map[string]interface{}
	mu      sync.Mutex
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

func NewCore(enc zapcore.Encoder, enab zapcore.LevelEnabler, config Config) *Core {
	if config.BatchSize == 0 {
		config.BatchSize = 100
	}
	if config.FlushInterval == 0 {
		config.FlushInterval = 5 * time.Second
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{
			Timeout: 10 * time.Second,
		}
	}

	core := &Core{
		LevelEnabler: enab,
		encoder:      enc,
		config:       config,
		buffer:       make([]map[string]interface{}, 0, config.BatchSize),
		stopCh:       make(chan struct{}),
	}

	core.wg.Add(1)
	go core.flushWorker()

	return core
}

func (c *Core) With(fields []zapcore.Field) zapcore.Core {
	clone := c.clone()
	clone.encoder = c.encoder.Clone()
	for _, field := range fields {
		field.AddTo(clone.encoder)
	}
	return clone
}

func (c *Core) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return ce.AddCore(entry, c)
	}
	return ce
}

func (c *Core) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	buf, err := c.encoder.EncodeEntry(entry, fields)
	if err != nil {
		return err
	}
	defer buf.Free()

	var logEntry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		return err
	}

	logEntry["dt"] = entry.Time.Format(time.RFC3339Nano)

	c.mu.Lock()
	c.buffer = append(c.buffer, logEntry)
	shouldFlush := len(c.buffer) >= c.config.BatchSize
	c.mu.Unlock()

	if shouldFlush {
		return c.flush()
	}

	return nil
}

func (c *Core) Sync() error {
	return c.flush()
}

func (c *Core) Close() error {
	close(c.stopCh)
	c.wg.Wait()
	return c.flush()
}

func (c *Core) clone() *Core {
	return &Core{
		LevelEnabler: c.LevelEnabler,
		encoder:      c.encoder.Clone(),
		config:       c.config,
		buffer:       c.buffer,
		mu:           sync.Mutex{},
		stopCh:       c.stopCh,
	}
}

func (c *Core) flushWorker() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.config.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.flush()
		case <-c.stopCh:
			return
		}
	}
}

func (c *Core) flush() error {
	c.mu.Lock()
	if len(c.buffer) == 0 {
		c.mu.Unlock()
		return nil
	}

	logs := make([]map[string]interface{}, len(c.buffer))
	copy(logs, c.buffer)
	c.buffer = c.buffer[:0]
	c.mu.Unlock()

	return c.sendToLogtail(logs)
}

func (c *Core) sendToLogtail(logs []map[string]interface{}) error {
	if len(logs) == 0 {
		return nil
	}

	payload, err := json.Marshal(logs)
	if err != nil {
		return fmt.Errorf("failed to marshal logs: %w", err)
	}

	req, err := http.NewRequest("POST", logtailURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)

	resp, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send logs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("logtail returned status %d", resp.StatusCode)
	}

	return nil
}
