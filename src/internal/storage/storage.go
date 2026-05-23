package storage

import (
	"context"
	"time"
)

type PtpTopologySnapshot struct {
	LocalClockIdentity  string
	ParentClockIdentity string
	ParentPort          int32
	GrandmasterIdentity string
	StepsRemoved        int32
	PathDelayNs         int64
	ChildPort           int32
}

type ReportStats struct {
	Hostname string
	Protocol string
	Metric   string
	Unit     string
	Samples  int64
	Min      float64
	Max      float64
	Mean     float64
	RMS      float64
	HasData  bool
}

type Storage interface {
	GetNodeIDList(ctx context.Context) ([]int32, error)
	ResolveNodeID(hostname string) (int32, bool)
	ResolveSensorID(ctx context.Context, nodeID int32, sensor, label string) (int32, error)

	InsertPtp4l(ctx context.Context, ts time.Time, nodeID int32, offsetNs, frequency, pathDelay int64) error
	InsertPhc2sys(ctx context.Context, ts time.Time, nodeID int32, offsetNs, frequency, pathDelay int64) error
	InsertPps(ctx context.Context, ts time.Time, nodeID int32, offsetNs int64) error
	InsertNetwork(ctx context.Context, ts time.Time, nodeID int32, rxPackets, txPackets, rxDropped, txDropped, rxErrors, txErrors, collisions int64) error
	InsertCpu(ctx context.Context, ts time.Time, nodeID int32, usagePercent float64, ctxSwitches, interrupts, softirqs int64) error
	InsertMemory(ctx context.Context, ts time.Time, nodeID int32, memAvailableKb float64, memFreeKb, swapTotalKb, swapFreeKb, buffersKb int64) error
	InsertTemperature(ctx context.Context, ts time.Time, sensorID int32, temperature int32) error
	UpdateNodeInfo(ctx context.Context, hostname string, role string, net_interface string, adapterName string) error
	InsertNode(ctx context.Context, hostname string) error
	InsertPtp4lPortEvent(ctx context.Context, ts time.Time, nodeID int32, portNum int32, portName string, from_state, to_state, event_trigger string) error
	InsertPtpTopologySnapshot(ctx context.Context, ts time.Time, nodeID int32, snapshot PtpTopologySnapshot) error

	GetOffsets(ctx context.Context, table string, nodeID int32, period time.Duration) ([]OffsetRow, error)
	InsertSlideMetrics(ctx context.Context, table string, ts time.Time, nodeID int32, windowSize int, mtie int64, tdev float64, adev float64) error

	TouchNode(nodeID int32)
	DeactivateStaleNodes(ctx context.Context, timeout time.Duration) error

	Close()
}

type ReportStatsLoader interface {
	LoadReportStats(ctx context.Context, from, to time.Time) ([]ReportStats, error)
}