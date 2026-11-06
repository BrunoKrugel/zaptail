package zaptail_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BrunoKrugel/zaptail"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type logSink struct {
	entries []map[string]any
	mu      sync.Mutex
}

func (l *logSink) add(batch []map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, batch...)
}

func (l *logSink) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

func (l *logSink) entryAt(index int) map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.entries[index]
}

func (l *logSink) messageCounts() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	counts := make(map[string]int, len(l.entries))
	for _, entry := range l.entries {
		message, _ := entry["msg"].(string)
		counts[message]++
	}
	return counts
}

type reportRecorder struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (r *reportRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *reportRecorder) Sync() error {
	return nil
}

func (r *reportRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func jsonEncoder() zapcore.Encoder {
	return zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
}

func logEntry(message string) zapcore.Entry {
	return zapcore.Entry{Level: zapcore.InfoLevel, Message: message, Time: time.Now()}
}

func startServer(t *testing.T, sink *logSink) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deliverBatch(t, r, sink)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)
	return server
}

func deliverBatch(t *testing.T, r *http.Request, sink *logSink) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	var batch []map[string]any
	require.NoError(t, json.Unmarshal(body, &batch))
	sink.add(batch)
}

func newTestCore(t *testing.T, serverURL string, mutate func(*zaptail.Config)) *zaptail.Core {
	t.Helper()
	config := &zaptail.Config{
		APIKey:        "test-key",
		LogtailURL:    serverURL,
		FlushInterval: time.Hour,
	}
	if mutate != nil {
		mutate(config)
	}
	core, err := zaptail.NewCore(jsonEncoder(), zapcore.InfoLevel, config)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, core.Close())
	})
	return core
}

func Test_NewCore_Validation(t *testing.T) {
	validConfig := func() *zaptail.Config {
		return &zaptail.Config{APIKey: "key", LogtailURL: "https://logs.example.com"}
	}

	tests := []struct {
		name        string
		mutate      func(*zaptail.Config)
		expectedErr string
	}{
		{
			name:        "missing api key",
			mutate:      func(c *zaptail.Config) { c.APIKey = "" },
			expectedErr: "APIKey is required",
		},
		{
			name:        "missing logtail url",
			mutate:      func(c *zaptail.Config) { c.LogtailURL = "" },
			expectedErr: "LogtailURL is required",
		},
		{
			name:        "invalid logtail url",
			mutate:      func(c *zaptail.Config) { c.LogtailURL = "not a url" },
			expectedErr: "invalid LogtailURL",
		},
		{
			name:        "negative batch size",
			mutate:      func(c *zaptail.Config) { c.BatchSize = -1 },
			expectedErr: "BatchSize must not be negative",
		},
		{
			name:        "negative max buffered logs",
			mutate:      func(c *zaptail.Config) { c.MaxBufferedLogs = -1 },
			expectedErr: "MaxBufferedLogs must not be negative",
		},
		{
			name:        "negative flush interval",
			mutate:      func(c *zaptail.Config) { c.FlushInterval = -time.Second },
			expectedErr: "FlushInterval must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := validConfig()
			tt.mutate(config)
			_, err := zaptail.NewCore(jsonEncoder(), zapcore.InfoLevel, config)
			require.ErrorContains(t, err, tt.expectedErr)
		})
	}

	t.Run("nil encoder", func(t *testing.T) {
		_, err := zaptail.NewCore(nil, zapcore.InfoLevel, validConfig())
		require.ErrorContains(t, err, "encoder is required")
	})

	t.Run("nil level enabler", func(t *testing.T) {
		_, err := zaptail.NewCore(jsonEncoder(), nil, validConfig())
		require.ErrorContains(t, err, "level enabler is required")
	})

	t.Run("nil config", func(t *testing.T) {
		_, err := zaptail.NewCore(jsonEncoder(), zapcore.InfoLevel, nil)
		require.ErrorContains(t, err, "config is required")
	})
}

func Test_Write_FlushesOnBatchSize(t *testing.T) {
	sink := &logSink{}
	server := startServer(t, sink)
	core := newTestCore(t, server.URL, func(c *zaptail.Config) { c.BatchSize = 3 })

	for _, message := range []string{"a", "b", "c"} {
		require.NoError(t, core.Write(logEntry(message), nil))
	}

	require.Eventually(t, func() bool { return sink.count() == 3 }, 5*time.Second, 10*time.Millisecond)
	counts := sink.messageCounts()
	for _, message := range []string{"a", "b", "c"} {
		require.Equal(t, 1, counts[message])
	}
}

func Test_Write_FlushesOnInterval(t *testing.T) {
	sink := &logSink{}
	server := startServer(t, sink)
	core := newTestCore(t, server.URL, func(c *zaptail.Config) { c.FlushInterval = 50 * time.Millisecond })

	require.NoError(t, core.Write(logEntry("tick"), nil))
	require.Eventually(t, func() bool { return sink.count() == 1 }, 5*time.Second, 10*time.Millisecond)
}

func Test_DefaultBatchSizeFlushesAt100(t *testing.T) {
	sink := &logSink{}
	server := startServer(t, sink)
	core := newTestCore(t, server.URL, nil)

	for i := range 100 {
		require.NoError(t, core.Write(logEntry(fmt.Sprintf("log-%d", i)), nil))
	}
	require.Eventually(t, func() bool { return sink.count() == 100 }, 5*time.Second, 10*time.Millisecond)
}

func Test_With_KeepsAllLogs(t *testing.T) {
	sink := &logSink{}
	server := startServer(t, sink)
	core := newTestCore(t, server.URL, func(c *zaptail.Config) { c.BatchSize = 10 })
	child := core.With([]zapcore.Field{zap.String("source", "child")})

	for i := range 50 {
		require.NoError(t, core.Write(logEntry(fmt.Sprintf("p-%d", i)), nil))
		require.NoError(t, child.Write(logEntry(fmt.Sprintf("c-%d", i)), nil))
	}
	require.NoError(t, core.Sync())
	require.NoError(t, child.Sync())

	require.Eventually(t, func() bool { return sink.count() == 100 }, 10*time.Second, 10*time.Millisecond)
	counts := sink.messageCounts()
	require.Len(t, counts, 100)
	for i := range 50 {
		require.Equal(t, 1, counts[fmt.Sprintf("p-%d", i)])
		require.Equal(t, 1, counts[fmt.Sprintf("c-%d", i)])
	}
}

func Test_ConcurrentWrites_AreAllDelivered(t *testing.T) {
	sink := &logSink{}
	server := startServer(t, sink)
	const (
		goroutines   = 8
		perGoroutine = 50
		capacity     = goroutines * perGoroutine
	)
	core := newTestCore(t, server.URL, func(c *zaptail.Config) {
		c.BatchSize = 10
		c.MaxBufferedLogs = capacity
	})

	child := core.With(nil)

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		writer := zapcore.Core(core)
		if g%2 == 1 {
			writer = child
		}
		go func() {
			defer wg.Done()
			for i := range perGoroutine {
				_ = writer.Write(logEntry(fmt.Sprintf("g%d-%d", g, i)), nil)
			}
		}()
	}
	wg.Wait()

	require.NoError(t, core.Sync())
	require.Eventually(t, func() bool { return sink.count() == capacity }, 10*time.Second, 10*time.Millisecond)

	counts := sink.messageCounts()
	require.Len(t, counts, capacity)
	for g := range goroutines {
		for i := range perGoroutine {
			require.Equal(t, 1, counts[fmt.Sprintf("g%d-%d", g, i)])
		}
	}
}

func Test_Close_OnChildCoreDoesNotStopParentFlush(t *testing.T) {
	sink := &logSink{}
	server := startServer(t, sink)
	core := newTestCore(t, server.URL, func(c *zaptail.Config) { c.FlushInterval = 50 * time.Millisecond })
	child := core.With(nil)

	require.NoError(t, child.(*zaptail.Core).Close())
	require.NoError(t, core.Write(logEntry("after-child-close"), nil))
	require.Eventually(t, func() bool { return sink.count() == 1 }, 5*time.Second, 10*time.Millisecond)
}

func Test_Close_IsIdempotent(t *testing.T) {
	sink := &logSink{}
	server := startServer(t, sink)
	core := newTestCore(t, server.URL, nil)

	require.NoError(t, core.Close())
	require.NoError(t, core.Close())
}

func Test_Close_FlushesRemainingLogs(t *testing.T) {
	sink := &logSink{}
	server := startServer(t, sink)
	core := newTestCore(t, server.URL, nil)

	require.NoError(t, core.Write(logEntry("left-1"), nil))
	require.NoError(t, core.Write(logEntry("left-2"), nil))
	require.Equal(t, 0, sink.count())

	require.NoError(t, core.Close())
	require.Eventually(t, func() bool { return sink.count() == 2 }, 5*time.Second, 10*time.Millisecond)
}

func Test_Sync_FlushesImmediately(t *testing.T) {
	sink := &logSink{}
	server := startServer(t, sink)
	core := newTestCore(t, server.URL, nil)

	require.NoError(t, core.Write(logEntry("synced"), nil))
	require.NoError(t, core.Sync())
	require.Eventually(t, func() bool { return sink.count() == 1 }, 5*time.Second, 10*time.Millisecond)
}

func Test_Check_RespectsLevelEnabler(t *testing.T) {
	sink := &logSink{}
	server := startServer(t, sink)
	core := newTestCore(t, server.URL, nil)
	logger := zap.New(core)

	logger.Debug("debug")
	require.NoError(t, logger.Sync())
	require.Equal(t, 0, sink.count())

	logger.Info("info")
	require.NoError(t, logger.Sync())
	require.Eventually(t, func() bool { return sink.count() == 1 }, 5*time.Second, 10*time.Millisecond)
}

func Test_RetriesFailedSendsUntilSuccess(t *testing.T) {
	sink := &logSink{}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		deliverBatch(t, r, sink)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)
	core := newTestCore(t, server.URL, func(c *zaptail.Config) { c.BatchSize = 1 })

	require.NoError(t, core.Write(logEntry("retry-me"), nil))
	require.Eventually(t, func() bool {
		return sink.count() == 1 && requests.Load() == 3
	}, 10*time.Second, 10*time.Millisecond)
}

func Test_DropsBatchAfterRetriesExhausted(t *testing.T) {
	recorder := &reportRecorder{}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	core := newTestCore(t, server.URL, func(c *zaptail.Config) {
		c.BatchSize = 1
		c.ErrorOutput = recorder
	})

	require.NoError(t, core.Write(logEntry("lost"), nil))
	require.Eventually(t, func() bool {
		return requests.Load() == 3 && strings.Contains(recorder.String(), "dropping 1 logs")
	}, 10*time.Second, 10*time.Millisecond)
}

func Test_BufferFull_DropsIncomingAndReports(t *testing.T) {
	sink := &logSink{}
	recorder := &reportRecorder{}
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enteredOnce.Do(func() { close(entered) })
		<-release
		deliverBatch(t, r, sink)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)
	core := newTestCore(t, server.URL, func(c *zaptail.Config) {
		c.BatchSize = 1
		c.MaxBufferedLogs = 2
		c.ErrorOutput = recorder
	})

	require.NoError(t, core.Write(logEntry("in-flight"), nil))
	<-entered

	require.NoError(t, core.Write(logEntry("buffered-1"), nil))
	require.NoError(t, core.Write(logEntry("buffered-2"), nil))
	require.NoError(t, core.Write(logEntry("dropped-1"), nil))
	require.NoError(t, core.Write(logEntry("dropped-2"), nil))

	close(release)
	require.NoError(t, core.Sync())

	require.Eventually(t, func() bool { return sink.count() == 3 }, 5*time.Second, 10*time.Millisecond)
	require.Contains(t, recorder.String(), "dropped 2 logs")
}

func Test_Context_CancellationDropsPendingLogs(t *testing.T) {
	recorder := &reportRecorder{}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	core, err := zaptail.NewCore(jsonEncoder(), zapcore.InfoLevel, &zaptail.Config{
		APIKey:        "test-key",
		LogtailURL:    server.URL,
		Context:       ctx,
		BatchSize:     1,
		FlushInterval: time.Hour,
		ErrorOutput:   recorder,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, core.Close()) })

	require.NoError(t, core.Write(logEntry("cancelled"), nil))
	require.Eventually(t, func() bool {
		return requests.Load() == 0 && strings.Contains(recorder.String(), "dropping 1 logs")
	}, 5*time.Second, 10*time.Millisecond)
}

func Test_DtField_UserValueIsPreserved(t *testing.T) {
	sink := &logSink{}
	server := startServer(t, sink)
	core := newTestCore(t, server.URL, nil)

	fields := []zapcore.Field{zap.String("dt", "2020-01-01T00:00:00Z")}
	require.NoError(t, core.Write(logEntry("custom-dt"), fields))
	require.NoError(t, core.Write(logEntry("auto-dt"), nil))
	require.NoError(t, core.Sync())

	require.Eventually(t, func() bool { return sink.count() == 2 }, 5*time.Second, 10*time.Millisecond)

	byMessage := make(map[string]map[string]any, 2)
	for i := range sink.count() {
		entry := sink.entryAt(i)
		message, _ := entry["msg"].(string)
		byMessage[message] = entry
	}

	require.Equal(t, "2020-01-01T00:00:00Z", byMessage["custom-dt"]["dt"])
	autoDt, ok := byMessage["auto-dt"]["dt"].(string)
	require.True(t, ok)
	_, err := time.Parse(time.RFC3339Nano, autoDt)
	require.NoError(t, err)
}
