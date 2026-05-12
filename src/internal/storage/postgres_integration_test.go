package storage

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"timesync-analyzer/src/internal/config"

	"go.uber.org/zap"
)

func TestPostgresStorageIntegration(t *testing.T) {
	if os.Getenv("TIMESYNC_ANALYZER_INTEGRATION") != "1" {
		t.Skip("set TIMESYNC_ANALYZER_INTEGRATION=1 and DB_* env vars to run")
	}

	cfg := integrationDBConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store, err := NewPostgresStorage(ctx, cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("NewPostgresStorage(): %v", err)
	}

	hostname := fmt.Sprintf("test-node-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupIntegrationNode(context.Background(), t, store, hostname)
		store.Close()
	})

	if err := store.InsertNode(ctx, hostname); err != nil {
		t.Fatalf("InsertNode(): %v", err)
	}

	nodeID, ok := store.ResolveNodeID(hostname)
	if !ok {
		t.Fatalf("ResolveNodeID() did not find inserted node")
	}

	if err := store.UpdateNodeInfo(ctx, hostname, "master", "eth-test0", "test-adapter"); err != nil {
		t.Fatalf("UpdateNodeInfo(): %v", err)
	}

	sensorID, err := store.ResolveSensorID(ctx, nodeID, "coretemp", "package")
	if err != nil {
		t.Fatalf("ResolveSensorID(): %v", err)
	}

	cachedSensorID, err := store.ResolveSensorID(ctx, nodeID, "coretemp", "package")
	if err != nil {
		t.Fatalf("ResolveSensorID() cached: %v", err)
	}
	if cachedSensorID != sensorID {
		t.Fatalf("cached sensor id=%d, want %d", cachedSensorID, sensorID)
	}

	ts := time.Now().UTC()
	if err := store.InsertPtp4l(ctx, ts, nodeID, -100, 20, 30); err != nil {
		t.Fatalf("InsertPtp4l(): %v", err)
	}
	if err := store.InsertPhc2sys(ctx, ts, nodeID, -101, 21, 31); err != nil {
		t.Fatalf("InsertPhc2sys(): %v", err)
	}
	if err := store.InsertPps(ctx, ts, nodeID, -102); err != nil {
		t.Fatalf("InsertPps(): %v", err)
	}
	if err := store.InsertNetwork(ctx, ts, nodeID, 1, 2, 3, 4, 5, 6, 7); err != nil {
		t.Fatalf("InsertNetwork(): %v", err)
	}
	if err := store.InsertCpu(ctx, ts, nodeID, 12.5, 10, 20, 30); err != nil {
		t.Fatalf("InsertCpu(): %v", err)
	}
	if err := store.InsertMemory(ctx, ts, nodeID, 100, 90, 80, 70, 60); err != nil {
		t.Fatalf("InsertMemory(): %v", err)
	}
	if err := store.InsertTemperature(ctx, ts, sensorID, 42000); err != nil {
		t.Fatalf("InsertTemperature(): %v", err)
	}
	if err := store.InsertPtp4lPortEvent(ctx, ts, nodeID, 1, "eth-test0", "LISTENING", "MASTER", "RS_MASTER"); err != nil {
		t.Fatalf("InsertPtp4lPortEvent(): %v", err)
	}

	offsets, err := store.GetOffsets(ctx, "ptp4l_metrics", nodeID, time.Hour)
	if err != nil {
		t.Fatalf("GetOffsets(): %v", err)
	}
	if len(offsets) == 0 {
		t.Fatalf("GetOffsets() returned no rows")
	}
	if offsets[len(offsets)-1].OffsetNs != -100 {
		t.Fatalf("last offset=%d, want -100", offsets[len(offsets)-1].OffsetNs)
	}

	store.TouchNode(nodeID)
	store.lastSeenMu.Lock()
	store.lastSeen[nodeID] = time.Now().Add(-time.Hour)
	store.lastSeenMu.Unlock()

	if err := store.DeactivateStaleNodes(ctx, time.Second); err != nil {
		t.Fatalf("DeactivateStaleNodes(): %v", err)
	}

	var active bool
	err = store.pool.QueryRow(ctx, `SELECT is_active FROM timesync.nodes WHERE node_id = $1`, nodeID).Scan(&active)
	if err != nil {
		t.Fatalf("query node active status: %v", err)
	}
	if active {
		t.Fatalf("node is still active after stale deactivation")
	}
}

func integrationDBConfig(t *testing.T) config.DBConfig {
	t.Helper()

	port := 5432
	if value := os.Getenv("DB_PORT"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("parse DB_PORT: %v", err)
		}
		port = parsed
	}

	cfg := config.DBConfig{
		Host:     os.Getenv("DB_HOST"),
		Port:     port,
		User:     os.Getenv("APP_USER"),
		Password: os.Getenv("APP_PASSWORD"),
		DBName:   os.Getenv("POSTGRES_DB"),
		SSLMode:  "disable",
	}
	if value := os.Getenv("DB_SSLMODE"); value != "" {
		cfg.SSLMode = value
	}

	if cfg.Host == "" || cfg.User == "" || cfg.Password == "" || cfg.DBName == "" {
		t.Skip("DB_HOST, APP_USER, APP_PASSWORD and POSTGRES_DB are required")
	}
	return cfg
}

func cleanupIntegrationNode(ctx context.Context, t *testing.T, store *PostgresStorage, hostname string) {
	t.Helper()

	nodeID, ok := store.ResolveNodeID(hostname)
	if !ok {
		return
	}

	statements := []string{
		`DELETE FROM timesync.temperature_metrics WHERE sensor_id IN (SELECT sensor_id FROM timesync.sensors WHERE node_id = $1)`,
		`DELETE FROM timesync.sensors WHERE node_id = $1`,
		`DELETE FROM timesync.quality_metrics WHERE node_id = $1`,
		`DELETE FROM timesync.ptp4l_port_events WHERE node_id = $1`,
		`DELETE FROM timesync.pps_metrics WHERE node_id = $1`,
		`DELETE FROM timesync.phc2sys_metrics WHERE node_id = $1`,
		`DELETE FROM timesync.ptp4l_metrics WHERE node_id = $1`,
		`DELETE FROM timesync.network_metrics WHERE node_id = $1`,
		`DELETE FROM timesync.cpu_metrics WHERE node_id = $1`,
		`DELETE FROM timesync.memory_metrics WHERE node_id = $1`,
		`DELETE FROM timesync.nodes WHERE node_id = $1`,
	}

	for _, statement := range statements {
		if _, err := store.pool.Exec(ctx, statement, nodeID); err != nil {
			t.Logf("cleanup query failed: %v", err)
		}
	}
}
