package storage

import (
	"context"
	"time"
)

type Storage interface {
	ResolveNodeID(hostname string) (int32, bool)
	ResolveSensorID(ctx context.Context, nodeID int32, sensor, label string) (int32, error)

	InsertPtp4l(ctx context.Context, ts time.Time, nodeID int32, offsetNs, frequency, pathDelay int64) error
	InsertPhc2sys(ctx context.Context, ts time.Time, nodeID int32, offsetNs, frequency, pathDelay int64) error
	InsertNetwork(ctx context.Context, ts time.Time, nodeID int32, rxPackets, txPackets, rxDropped, txDropped, rxErrors, txErrors, collisions int64) error
	InsertCpu(ctx context.Context, ts time.Time, nodeID int32, usagePercent float64, ctxSwitches, interrupts, softirqs int64) error
	InsertMemory(ctx context.Context, ts time.Time, nodeID int32, memAvailableKb float64, memFreeKb, swapTotalKb, swapFreeKb, buffersKb int64) error
	InsertTemperature(ctx context.Context, ts time.Time, sensorID int32, temperature int32) error

	Close()
}
