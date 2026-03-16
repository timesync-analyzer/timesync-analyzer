package storage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"timesync-analyzer/src/internal/config"
)

type sensorKey struct {
	nodeID int32
	sensor string
	label  string
}

type PostgresStorage struct {
	pool        *pgxpool.Pool
	logger      *zap.Logger
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

func (s *PostgresStorage) InsertNodeInfo(ctx context.Context, hostname string, netInterface string, ipAddress string, nodeType string) error {
	var nodeID int32
	err := s.pool.QueryRow(ctx,
		`INSERT INTO timesync.nodes (hostname, interface, ip_address, type, is_active, last_seen_at)
			VALUES ($1, $2, $3, $4, TRUE, NOW())
			ON CONFLICT (hostname) DO UPDATE SET
				interface = EXCLUDED.interface,
				ip_address = EXCLUDED.ip_address,
				type = EXCLUDED.type,
				is_active = TRUE,
				last_seen_at = NOW()
			RETURNING node_id`,
		hostname, netInterface, ipAddress, nodeType,
	).Scan(&nodeID)
	if err != nil {
		return fmt.Errorf("insert node info: %w", err)
	}

	s.nodeCache[hostname] = nodeID
	s.TouchNode(nodeID)
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
	id, ok := s.nodeCache[hostname]
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
		 VALUES ($1, $2, $3, $4, $5)`,
		ts, nodeID, offsetNs, frequency, pathDelay,
	)
	return err
}

func (s *PostgresStorage) InsertPps(ctx context.Context, ts time.Time, nodeID int32, offsetNs int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO timesync.pps_metrics (time, node_id, offset_ns)
		 VALUES ($1, $2, $3)`,
		ts, nodeID, offsetNs,
	)
	return err
}

func (s *PostgresStorage) InsertPhc2sys(ctx context.Context, ts time.Time, nodeID int32, offsetNs, frequency, pathDelay int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO timesync.phc2sys_metrics (time, node_id, offset_ns, frequency, path_delay)
		 VALUES ($1, $2, $3, $4, $5)`,
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

func (s *PostgresStorage) Close() {
	s.pool.Close()
}
