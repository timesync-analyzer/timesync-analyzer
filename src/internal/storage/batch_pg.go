package storage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"timesync-analyzer/src/internal/config"
)

type ptp4lRow struct {
	ts        time.Time
	nodeID    int32
	offsetNs  int64
	frequency int64
	pathDelay int64
}

type phc2sysRow struct {
	ts        time.Time
	nodeID    int32
	offsetNs  int64
	frequency int64
	pathDelay int64
}

type ppsRow struct {
	ts       time.Time
	nodeID   int32
	offsetNs int64
}

type networkRow struct {
	ts         time.Time
	nodeID     int32
	rxPackets  int64
	txPackets  int64
	rxDropped  int64
	txDropped  int64
	rxErrors   int64
	txErrors   int64
	collisions int64
}

type cpuRow struct {
	ts           time.Time
	nodeID       int32
	usagePercent float64
	ctxSwitches  int64
	interrupts   int64
	softirqs     int64
}

type memoryRow struct {
	ts             time.Time
	nodeID         int32
	memAvailableKb float64
	memFreeKb      int64
	swapTotalKb    int64
	swapFreeKb     int64
	buffersKb      int64
}

type temperatureRow struct {
	ts          time.Time
	sensorID    int32
	temperature int32
}

type BatchPostgresStorage struct {
	*PostgresStorage
	maxSize       int
	flushInterval time.Duration

	ptp4lMu  sync.Mutex
	ptp4lBuf []ptp4lRow

	phc2sysMu  sync.Mutex
	phc2sysBuf []phc2sysRow

	ppsMu  sync.Mutex
	ppsBuf []ppsRow

	networkMu  sync.Mutex
	networkBuf []networkRow

	cpuMu  sync.Mutex
	cpuBuf []cpuRow

	memoryMu  sync.Mutex
	memoryBuf []memoryRow

	temperatureMu  sync.Mutex
	temperatureBuf []temperatureRow
}

const (
	defaultMaxSize       = 500
	defaultFlushInterval = time.Second
)

func NewBatchPostgresStorage(ctx context.Context, cfg config.DBConfig, logger *zap.Logger) (*BatchPostgresStorage, error) {
	pgStorage, err := NewPostgresStorage(ctx, cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("can't create postgres handler: %w", err)
	}

	maxSize := cfg.Batch.MaxSize
	if maxSize <= 0 {
		maxSize = defaultMaxSize
	}

	flushInterval := cfg.Batch.FlushInterval
	if flushInterval <= 0 {
		flushInterval = defaultFlushInterval
	}

	return &BatchPostgresStorage{
		PostgresStorage: pgStorage,
		maxSize:         maxSize,
		flushInterval:   flushInterval,
		ptp4lBuf:        make([]ptp4lRow, 0, maxSize),
		phc2sysBuf:      make([]phc2sysRow, 0, maxSize),
		ppsBuf:          make([]ppsRow, 0, maxSize),
		networkBuf:      make([]networkRow, 0, maxSize),
		cpuBuf:          make([]cpuRow, 0, maxSize),
		memoryBuf:       make([]memoryRow, 0, maxSize),
		temperatureBuf:  make([]temperatureRow, 0, maxSize),
	}, nil
}

func (s *BatchPostgresStorage) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.FlushAll(shutdownCtx)
			return nil
		case <-ticker.C:
			if err := s.FlushAll(ctx); err != nil {
				s.logger.Error("periodic flush failed", zap.Error(err))
			}
		}
	}
}

func (s *BatchPostgresStorage) FlushAll(ctx context.Context) error {
	if err := s.flushPtp4l(ctx); err != nil {
		return err
	}
	if err := s.flushPhc2sys(ctx); err != nil {
		return err
	}
	if err := s.flushPPS(ctx); err != nil {
		return err
	}
	if err := s.flushNetwork(ctx); err != nil {
		return err
	}
	if err := s.flushCpu(ctx); err != nil {
		return err
	}
	if err := s.flushMemory(ctx); err != nil {
		return err
	}
	if err := s.flushTemperature(ctx); err != nil {
		return err
	}
	return nil
}

func (s *BatchPostgresStorage) copyViaTempTable(ctx context.Context, createTempSQL string, tempTable string, columns []string, rows pgx.CopyFromSource, insertSQL string) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if _, err := tx.Exec(ctx, createTempSQL); err != nil {
		return err
	}

	if _, err := tx.CopyFrom(ctx, pgx.Identifier{tempTable}, columns, rows); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, insertSQL); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	committed = true
	return nil
}

func (s *BatchPostgresStorage) InsertPtp4l(ctx context.Context, ts time.Time, nodeID int32, offsetNs, frequency, pathDelay int64) error {
	s.ptp4lMu.Lock()
	s.ptp4lBuf = append(s.ptp4lBuf, ptp4lRow{ts, nodeID, offsetNs, frequency, pathDelay})
	shouldFlush := len(s.ptp4lBuf) >= s.maxSize
	s.ptp4lMu.Unlock()

	if shouldFlush {
		return s.flushPtp4l(ctx)
	}
	return nil
}

func (s *BatchPostgresStorage) flushPtp4l(ctx context.Context) error {
	s.ptp4lMu.Lock()
	rows := s.ptp4lBuf
	s.ptp4lBuf = make([]ptp4lRow, 0, s.maxSize)
	s.ptp4lMu.Unlock()

	if len(rows) == 0 {
		return nil
	}

	err := s.copyViaTempTable(ctx,
		`CREATE TEMP TABLE batch_ptp4l_metrics (LIKE timesync.ptp4l_metrics INCLUDING DEFAULTS) ON COMMIT DROP`,
		"batch_ptp4l_metrics",
		[]string{"time", "node_id", "offset_ns", "frequency", "path_delay"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			r := rows[i]
			return []any{r.ts, r.nodeID, r.offsetNs, r.frequency, r.pathDelay}, nil
		}),
		`INSERT INTO timesync.ptp4l_metrics (time, node_id, offset_ns, frequency, path_delay)
		 SELECT time, node_id, offset_ns, frequency, path_delay
		 FROM batch_ptp4l_metrics
		 ON CONFLICT (node_id, time) DO NOTHING`,
	)
	if err != nil {
		s.ptp4lMu.Lock()
		s.ptp4lBuf = append(rows, s.ptp4lBuf...)
		s.ptp4lMu.Unlock()
		s.logger.Error("batch flush ptp4l failed", zap.Int("rows", len(rows)), zap.Error(err))
		return err
	}
	return nil
}

func (s *BatchPostgresStorage) InsertPhc2sys(ctx context.Context, ts time.Time, nodeID int32, offsetNs, frequency, pathDelay int64) error {
	s.phc2sysMu.Lock()
	s.phc2sysBuf = append(s.phc2sysBuf, phc2sysRow{ts, nodeID, offsetNs, frequency, pathDelay})
	shouldFlush := len(s.phc2sysBuf) >= s.maxSize
	s.phc2sysMu.Unlock()

	if shouldFlush {
		return s.flushPhc2sys(ctx)
	}
	return nil
}

func (s *BatchPostgresStorage) flushPhc2sys(ctx context.Context) error {
	s.phc2sysMu.Lock()
	rows := s.phc2sysBuf
	s.phc2sysBuf = make([]phc2sysRow, 0, s.maxSize)
	s.phc2sysMu.Unlock()

	if len(rows) == 0 {
		return nil
	}

	err := s.copyViaTempTable(ctx,
		`CREATE TEMP TABLE batch_phc2sys_metrics (LIKE timesync.phc2sys_metrics INCLUDING DEFAULTS) ON COMMIT DROP`,
		"batch_phc2sys_metrics",
		[]string{"time", "node_id", "offset_ns", "frequency", "path_delay"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			r := rows[i]
			return []any{r.ts, r.nodeID, r.offsetNs, r.frequency, r.pathDelay}, nil
		}),
		`INSERT INTO timesync.phc2sys_metrics (time, node_id, offset_ns, frequency, path_delay)
		 SELECT time, node_id, offset_ns, frequency, path_delay
		 FROM batch_phc2sys_metrics
		 ON CONFLICT (node_id, time) DO NOTHING`,
	)
	if err != nil {
		s.phc2sysMu.Lock()
		s.phc2sysBuf = append(rows, s.phc2sysBuf...)
		s.phc2sysMu.Unlock()
		s.logger.Error("batch flush phc2sys failed", zap.Int("rows", len(rows)), zap.Any("rows_data", rows), zap.Error(err))
		return err
	}
	return nil
}

func (s *BatchPostgresStorage) InsertPps(ctx context.Context, ts time.Time, nodeID int32, offsetNs int64) error {
	s.ppsMu.Lock()
	s.ppsBuf = append(s.ppsBuf, ppsRow{ts, nodeID, offsetNs})
	shouldFlush := len(s.ppsBuf) >= s.maxSize
	s.ppsMu.Unlock()

	if shouldFlush {
		return s.flushPPS(ctx)
	}
	return nil
}

func (s *BatchPostgresStorage) flushPPS(ctx context.Context) error {
	s.ppsMu.Lock()
	rows := s.ppsBuf
	s.ppsBuf = make([]ppsRow, 0, s.maxSize)
	s.ppsMu.Unlock()

	if len(rows) == 0 {
		return nil
	}

	err := s.copyViaTempTable(ctx,
		`CREATE TEMP TABLE batch_pps_metrics (LIKE timesync.pps_metrics INCLUDING DEFAULTS) ON COMMIT DROP`,
		"batch_pps_metrics",
		[]string{"time", "node_id", "offset_ns"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			r := rows[i]
			return []any{r.ts, r.nodeID, r.offsetNs}, nil
		}),
		`INSERT INTO timesync.pps_metrics (time, node_id, offset_ns)
		 SELECT time, node_id, offset_ns
		 FROM batch_pps_metrics
		 ON CONFLICT (node_id, time) DO NOTHING`,
	)
	if err != nil {
		s.ppsMu.Lock()
		s.ppsBuf = append(rows, s.ppsBuf...)
		s.ppsMu.Unlock()
		s.logger.Error("batch flush pps failed", zap.Int("rows", len(rows)), zap.Error(err))
		return err
	}
	return nil
}

func (s *BatchPostgresStorage) InsertNetwork(ctx context.Context, ts time.Time, nodeID int32, rxPackets, txPackets, rxDropped, txDropped, rxErrors, txErrors, collisions int64) error {
	s.networkMu.Lock()
	s.networkBuf = append(s.networkBuf, networkRow{ts, nodeID, rxPackets, txPackets, rxDropped, txDropped, rxErrors, txErrors, collisions})
	shouldFlush := len(s.networkBuf) >= s.maxSize
	s.networkMu.Unlock()

	if shouldFlush {
		return s.flushNetwork(ctx)
	}
	return nil
}

func (s *BatchPostgresStorage) flushNetwork(ctx context.Context) error {
	s.networkMu.Lock()
	rows := s.networkBuf
	s.networkBuf = make([]networkRow, 0, s.maxSize)
	s.networkMu.Unlock()

	if len(rows) == 0 {
		return nil
	}

	err := s.copyViaTempTable(ctx,
		`CREATE TEMP TABLE batch_network_metrics (LIKE timesync.network_metrics INCLUDING DEFAULTS) ON COMMIT DROP`,
		"batch_network_metrics",
		[]string{"time", "node_id", "rx_packets", "tx_packets", "rx_dropped", "tx_dropped", "rx_errors", "tx_errors", "collisions"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			r := rows[i]
			return []any{r.ts, r.nodeID, r.rxPackets, r.txPackets, r.rxDropped, r.txDropped, r.rxErrors, r.txErrors, r.collisions}, nil
		}),
		`INSERT INTO timesync.network_metrics (time, node_id, rx_packets, tx_packets, rx_dropped, tx_dropped, rx_errors, tx_errors, collisions)
		 SELECT time, node_id, rx_packets, tx_packets, rx_dropped, tx_dropped, rx_errors, tx_errors, collisions
		 FROM batch_network_metrics
		 ON CONFLICT (node_id, time) DO NOTHING`,
	)
	if err != nil {
		s.networkMu.Lock()
		s.networkBuf = append(rows, s.networkBuf...)
		s.networkMu.Unlock()
		s.logger.Error("batch flush network failed", zap.Int("rows", len(rows)), zap.Error(err))
		return err
	}
	return nil
}

func (s *BatchPostgresStorage) InsertCpu(ctx context.Context, ts time.Time, nodeID int32, usagePercent float64, ctxSwitches, interrupts, softirqs int64) error {
	s.cpuMu.Lock()
	s.cpuBuf = append(s.cpuBuf, cpuRow{ts, nodeID, usagePercent, ctxSwitches, interrupts, softirqs})
	shouldFlush := len(s.cpuBuf) >= s.maxSize
	s.cpuMu.Unlock()

	if shouldFlush {
		return s.flushCpu(ctx)
	}
	return nil
}

func (s *BatchPostgresStorage) flushCpu(ctx context.Context) error {
	s.cpuMu.Lock()
	rows := s.cpuBuf
	s.cpuBuf = make([]cpuRow, 0, s.maxSize)
	s.cpuMu.Unlock()

	if len(rows) == 0 {
		return nil
	}

	err := s.copyViaTempTable(ctx,
		`CREATE TEMP TABLE batch_cpu_metrics (LIKE timesync.cpu_metrics INCLUDING DEFAULTS) ON COMMIT DROP`,
		"batch_cpu_metrics",
		[]string{"time", "node_id", "usage_percent", "context_switches", "interrupts", "softirqs"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			r := rows[i]
			return []any{r.ts, r.nodeID, r.usagePercent, r.ctxSwitches, r.interrupts, r.softirqs}, nil
		}),
		`INSERT INTO timesync.cpu_metrics (time, node_id, usage_percent, context_switches, interrupts, softirqs)
		 SELECT time, node_id, usage_percent, context_switches, interrupts, softirqs
		 FROM batch_cpu_metrics
		 ON CONFLICT (node_id, time) DO NOTHING`,
	)
	if err != nil {
		s.cpuMu.Lock()
		s.cpuBuf = append(rows, s.cpuBuf...)
		s.cpuMu.Unlock()
		s.logger.Error("batch flush cpu failed", zap.Int("rows", len(rows)), zap.Error(err))
		return err
	}
	return nil
}

func (s *BatchPostgresStorage) InsertMemory(ctx context.Context, ts time.Time, nodeID int32, memAvailableKb float64, memFreeKb, swapTotalKb, swapFreeKb, buffersKb int64) error {
	s.memoryMu.Lock()
	s.memoryBuf = append(s.memoryBuf, memoryRow{ts, nodeID, memAvailableKb, memFreeKb, swapTotalKb, swapFreeKb, buffersKb})
	shouldFlush := len(s.memoryBuf) >= s.maxSize
	s.memoryMu.Unlock()

	if shouldFlush {
		return s.flushMemory(ctx)
	}
	return nil
}

func (s *BatchPostgresStorage) flushMemory(ctx context.Context) error {
	s.memoryMu.Lock()
	rows := s.memoryBuf
	s.memoryBuf = make([]memoryRow, 0, s.maxSize)
	s.memoryMu.Unlock()

	if len(rows) == 0 {
		return nil
	}

	err := s.copyViaTempTable(ctx,
		`CREATE TEMP TABLE batch_memory_metrics (LIKE timesync.memory_metrics INCLUDING DEFAULTS) ON COMMIT DROP`,
		"batch_memory_metrics",
		[]string{"time", "node_id", "mem_available_kb", "mem_free_kb", "swap_total_kb", "swap_free_kb", "buffers_kb"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			r := rows[i]
			return []any{r.ts, r.nodeID, int64(r.memAvailableKb), r.memFreeKb, r.swapTotalKb, r.swapFreeKb, r.buffersKb}, nil
		}),
		`INSERT INTO timesync.memory_metrics (time, node_id, mem_available_kb, mem_free_kb, swap_total_kb, swap_free_kb, buffers_kb)
		 SELECT time, node_id, mem_available_kb, mem_free_kb, swap_total_kb, swap_free_kb, buffers_kb
		 FROM batch_memory_metrics
		 ON CONFLICT (node_id, time) DO NOTHING`,
	)
	if err != nil {
		s.memoryMu.Lock()
		s.memoryBuf = append(rows, s.memoryBuf...)
		s.memoryMu.Unlock()
		s.logger.Error("batch flush memory failed", zap.Int("rows", len(rows)), zap.Error(err))
		return err
	}
	return nil
}

func (s *BatchPostgresStorage) InsertTemperature(ctx context.Context, ts time.Time, sensorID int32, temperature int32) error {
	s.temperatureMu.Lock()
	s.temperatureBuf = append(s.temperatureBuf, temperatureRow{ts, sensorID, temperature})
	shouldFlush := len(s.temperatureBuf) >= s.maxSize
	s.temperatureMu.Unlock()

	if shouldFlush {
		return s.flushTemperature(ctx)
	}
	return nil
}

func (s *BatchPostgresStorage) flushTemperature(ctx context.Context) error {
	s.temperatureMu.Lock()
	rows := s.temperatureBuf
	s.temperatureBuf = make([]temperatureRow, 0, s.maxSize)
	s.temperatureMu.Unlock()

	if len(rows) == 0 {
		return nil
	}

	err := s.copyViaTempTable(ctx,
		`CREATE TEMP TABLE batch_temperature_metrics (LIKE timesync.temperature_metrics INCLUDING DEFAULTS) ON COMMIT DROP`,
		"batch_temperature_metrics",
		[]string{"time", "sensor_id", "temperature"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			r := rows[i]
			return []any{r.ts, r.sensorID, r.temperature}, nil
		}),
		`INSERT INTO timesync.temperature_metrics (time, sensor_id, temperature)
		 SELECT time, sensor_id, temperature
		 FROM batch_temperature_metrics`,
	)
	if err != nil {
		s.temperatureMu.Lock()
		s.temperatureBuf = append(rows, s.temperatureBuf...)
		s.temperatureMu.Unlock()
		s.logger.Error("batch flush temperature failed", zap.Int("rows", len(rows)), zap.Error(err))
		return err
	}
	return nil
}
