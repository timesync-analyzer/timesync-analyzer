package storage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"timesync-analyzer/src/internal/config"
)

type sensorKey struct {
	nodeID int32
	sensor string
	label  string
}

type OffsetRow struct {
	Time     time.Time
	OffsetNs int64
}

type PostgresStorage struct {
	pool        *pgxpool.Pool
	logger      *zap.Logger
	nodeMu      sync.RWMutex
	nodeCache   map[string]int32
	sensorCache map[sensorKey]int32
	sensorMu    sync.Mutex
	lastSeen    map[int32]time.Time
	lastSeenMu  sync.RWMutex
}

func NewPostgresStorage(ctx context.Context, cfg config.DBConfig, logger *zap.Logger) (*PostgresStorage, error) {
	pool, err := pgxpool.New(ctx, cfg.ConnString())
	if err != nil {
		return nil, fmt.Errorf("unable to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("unable to ping database: %w", err)
	}

	s := &PostgresStorage{
		pool:        pool,
		logger:      logger,
		nodeCache:   make(map[string]int32),
		sensorCache: make(map[sensorKey]int32),
		lastSeen:    make(map[int32]time.Time),
	}

	if err := s.loadNodeCache(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("unable to load node cache: %w", err)
	}

	logger.Info("Storage initialized", zap.Int("nodes_loaded", len(s.nodeCache)))
	return s, nil
}

func (s *PostgresStorage) InsertNode(ctx context.Context, hostname string) error {
	var nodeID int32
	err := s.pool.QueryRow(ctx,
		`INSERT INTO timesync.nodes (hostname, is_active, last_seen_at)
			VALUES ($1, TRUE, NOW())
			ON CONFLICT (hostname) DO UPDATE SET
				is_active = TRUE,
				last_seen_at = NOW()
			RETURNING node_id`,
		hostname,
	).Scan(&nodeID)
	if err != nil {
		return fmt.Errorf("insert node: %w", err)
	}

	s.nodeMu.Lock()
	s.nodeCache[hostname] = nodeID
	s.nodeMu.Unlock()
	s.TouchNode(nodeID)
	return nil
}

func (s *PostgresStorage) UpdateNodeInfo(ctx context.Context, hostname string, role string, netInterface string, adapterName string) error {
	var nodeID int32
	err := s.pool.QueryRow(ctx,
		`UPDATE timesync.nodes
			SET interface = $2,
				type = $3,
				adapter_name = $4,
				last_seen_at = NOW()
			WHERE hostname = $1
			RETURNING node_id`,
		hostname, netInterface, role, adapterName,
	).Scan(&nodeID)
	if err != nil {
		return fmt.Errorf("update node interface: %w", err)
	}

	s.nodeMu.Lock()
	s.nodeCache[hostname] = nodeID
	s.nodeMu.Unlock()
	return nil
}

func (s *PostgresStorage) loadNodeCache(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, "SELECT node_id, hostname FROM timesync.nodes")
	if err != nil {
		return fmt.Errorf("query nodes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int32
		var hostname string
		if err := rows.Scan(&id, &hostname); err != nil {
			return fmt.Errorf("scan node row: %w", err)
		}
		s.nodeCache[hostname] = id
	}
	return rows.Err()
}

func (s *PostgresStorage) ResolveNodeID(hostname string) (int32, bool) {
	s.nodeMu.RLock()
	id, ok := s.nodeCache[hostname]
	s.nodeMu.RUnlock()
	return id, ok
}

func (s *PostgresStorage) ResolveSensorID(ctx context.Context, nodeID int32, sensor, label string) (int32, error) {
	key := sensorKey{nodeID: nodeID, sensor: sensor, label: label}

	s.sensorMu.Lock()
	if id, ok := s.sensorCache[key]; ok {
		s.sensorMu.Unlock()
		return id, nil
	}
	s.sensorMu.Unlock()

	var sensorID int32
	err := s.pool.QueryRow(ctx,
		`INSERT INTO timesync.sensors (node_id, sensor_name, sensor_label)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (node_id, sensor_name, sensor_label) DO UPDATE SET sensor_name = EXCLUDED.sensor_name
		 RETURNING sensor_id`,
		nodeID, sensor, label,
	).Scan(&sensorID)
	if err != nil {
		return 0, fmt.Errorf("resolve sensor_id: %w", err)
	}

	s.sensorMu.Lock()
	s.sensorCache[key] = sensorID
	s.sensorMu.Unlock()

	return sensorID, nil
}

func (s *PostgresStorage) InsertPtp4l(ctx context.Context, ts time.Time, nodeID int32, offsetNs, frequency, pathDelay int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO timesync.ptp4l_metrics (time, node_id, offset_ns, frequency, path_delay)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (node_id, time) DO NOTHING`,
		ts, nodeID, offsetNs, frequency, pathDelay,
	)
	return err
}

func (s *PostgresStorage) InsertPtp4lPortEvent(ctx context.Context, ts time.Time, nodeID int32, portNum int32, portName string, fromState, toState, eventTrigger string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO timesync.ptp4l_port_events (time, node_id, port, interface, from_state, to_state, event_trigger)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (time, node_id, port) DO NOTHING`,
		ts, nodeID, portNum, portName, fromState, toState, eventTrigger,
	)
	return err
}

func (s *PostgresStorage) InsertPps(ctx context.Context, ts time.Time, nodeID int32, offsetNs int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO timesync.pps_metrics (time, node_id, offset_ns)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (node_id, time) DO NOTHING`,
		ts, nodeID, offsetNs,
	)
	return err
}

func (s *PostgresStorage) InsertPhc2sys(ctx context.Context, ts time.Time, nodeID int32, offsetNs, frequency, pathDelay int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO timesync.phc2sys_metrics (time, node_id, offset_ns, frequency, path_delay)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (node_id, time) DO NOTHING`,
		ts, nodeID, offsetNs, frequency, pathDelay,
	)
	return err
}

func (s *PostgresStorage) InsertNetwork(ctx context.Context, ts time.Time, nodeID int32, rxPackets, txPackets, rxDropped, txDropped, rxErrors, txErrors, collisions int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO timesync.network_metrics (time, node_id, rx_packets, tx_packets, rx_dropped, tx_dropped, rx_errors, tx_errors, collisions)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		ts, nodeID, rxPackets, txPackets, rxDropped, txDropped, rxErrors, txErrors, collisions,
	)
	return err
}

func (s *PostgresStorage) InsertCpu(ctx context.Context, ts time.Time, nodeID int32, usagePercent float64, ctxSwitches, interrupts, softirqs int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO timesync.cpu_metrics (time, node_id, usage_percent, context_switches, interrupts, softirqs)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		ts, nodeID, usagePercent, ctxSwitches, interrupts, softirqs,
	)
	return err
}

func (s *PostgresStorage) InsertMemory(ctx context.Context, ts time.Time, nodeID int32, memAvailableKb float64, memFreeKb, swapTotalKb, swapFreeKb, buffersKb int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO timesync.memory_metrics (time, node_id, mem_available_kb, mem_free_kb, swap_total_kb, swap_free_kb, buffers_kb)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		ts, nodeID, memAvailableKb, memFreeKb, swapTotalKb, swapFreeKb, buffersKb,
	)
	return err
}

func (s *PostgresStorage) InsertTemperature(ctx context.Context, ts time.Time, sensorID int32, temperature int32) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO timesync.temperature_metrics (time, sensor_id, temperature)
		 VALUES ($1, $2, $3)`,
		ts, sensorID, temperature,
	)
	return err
}

func (s *PostgresStorage) TouchNode(nodeID int32) {
	s.lastSeenMu.Lock()
	s.lastSeen[nodeID] = time.Now()
	s.lastSeenMu.Unlock()
}

func (s *PostgresStorage) DeactivateStaleNodes(ctx context.Context, timeout time.Duration) error {
	s.lastSeenMu.RLock()
	snapshot := make(map[int32]time.Time, len(s.lastSeen))
	for id, ts := range s.lastSeen {
		snapshot[id] = ts
	}
	s.lastSeenMu.RUnlock()

	now := time.Now()
	for nodeID, ts := range snapshot {
		if now.Sub(ts) > timeout {
			_, err := s.pool.Exec(ctx,
				`UPDATE timesync.nodes SET is_active = FALSE WHERE node_id = $1 AND is_active = TRUE`,
				nodeID,
			)
			if err != nil {
				return fmt.Errorf("deactivate node %d: %w", nodeID, err)
			}
		} else {
			_, err := s.pool.Exec(ctx,
				`UPDATE timesync.nodes SET is_active = TRUE, last_seen_at = $2 WHERE node_id = $1`,
				nodeID, ts,
			)
			if err != nil {
				return fmt.Errorf("update last_seen_at for node %d: %w", nodeID, err)
			}
		}
	}
	return nil
}

func (s *PostgresStorage) GetNodeIDList(ctx context.Context) ([]int32, error) {
	rows, err := s.pool.Query(ctx, "SELECT node_id FROM timesync.nodes WHERE is_active = true")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []int32
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan node row: %w", err)
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (s *PostgresStorage) GetOffsets(ctx context.Context, table string, nodeID int32, period time.Duration) ([]OffsetRow, error) {
	query := fmt.Sprintf(
		`SELECT time, offset_ns
		 FROM %s
		 WHERE node_id = $1 AND time > now() - $2::interval
		 ORDER BY time ASC`,
		pgx.Identifier{"timesync", table}.Sanitize(),
	)

	rows, err := s.pool.Query(ctx, query, nodeID, period.String())
	if err != nil {
		return nil, fmt.Errorf("query offsets: %w", err)
	}
	defer rows.Close()

	var result []OffsetRow
	for rows.Next() {
		var r OffsetRow
		if err := rows.Scan(&r.Time, &r.OffsetNs); err != nil {
			return nil, fmt.Errorf("scan offset row: %w", err)
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s *PostgresStorage) InsertSlideMetrics(ctx context.Context, protocol string, ts time.Time, nodeID int32, windowSize int, mtie int64, tdev float64, adev float64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO timesync.quality_metrics (time, sync_protocol, node_id, window_size_s, mtie_ns, tdev_ns, adev_ns)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		ts, protocol, nodeID, windowSize, mtie, tdev, adev,
	)

	if err != nil {
		s.logger.Error("can't insert quality metrics", zap.Error(err))
	}
	return err
}

func (s *PostgresStorage) Close() {
	s.pool.Close()
}