package app

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"timesync-analyzer/src/internal/config"
	"timesync-analyzer/src/internal/storage"

	pb "timesync-analyzer/protocol/generated"

	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type testSensorKey struct {
	nodeID int32
	sensor string
	label  string
}

type ptp4lInsert struct {
	ts        time.Time
	nodeID    int32
	offsetNs  int64
	frequency int64
	pathDelay int64
}

type phc2sysInsert struct {
	ts        time.Time
	nodeID    int32
	offsetNs  int64
	frequency int64
	pathDelay int64
}

type ppsInsert struct {
	ts       time.Time
	nodeID   int32
	offsetNs int64
}

type networkInsert struct {
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

type cpuInsert struct {
	ts           time.Time
	nodeID       int32
	usagePercent float64
	ctxSwitches  int64
	interrupts   int64
	softirqs     int64
}

type memoryInsert struct {
	ts             time.Time
	nodeID         int32
	memAvailableKb float64
	memFreeKb      int64
	swapTotalKb    int64
	swapFreeKb     int64
	buffersKb      int64
}

type temperatureInsert struct {
	ts          time.Time
	sensorID    int32
	temperature int32
}

type nodeInfoUpdate struct {
	hostname     string
	role         string
	netInterface string
	adapterName  string
}

type portEventInsert struct {
	ts           time.Time
	nodeID       int32
	portNum      int32
	portName     string
	fromState    string
	toState      string
	eventTrigger string
}

type slideMetricsInsert struct {
	table      string
	ts         time.Time
	nodeID     int32
	windowSize int
	mtie       int64
	tdev       float64
	adev       float64
}

type fakeStorage struct {
	mu                  sync.Mutex
	nextNodeID          int32
	nextSensorID        int32
	nodes               map[string]int32
	sensors             map[testSensorKey]int32
	lastSeen            map[int32]time.Time
	offsets             map[string]map[int32][]storage.OffsetRow
	ptp4lInserts        []ptp4lInsert
	phc2sysInserts      []phc2sysInsert
	ppsInserts          []ppsInsert
	networkInserts      []networkInsert
	cpuInserts          []cpuInsert
	memoryInserts       []memoryInsert
	temperatureInserts  []temperatureInsert
	nodeInfoUpdates     []nodeInfoUpdate
	portEventInserts    []portEventInsert
	slideMetricsInserts []slideMetricsInsert
	ptp4lInserted       chan struct{}
	ptp4lInsertedOnce   sync.Once
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{
		nextNodeID:    1,
		nextSensorID:  1,
		nodes:         make(map[string]int32),
		sensors:       make(map[testSensorKey]int32),
		lastSeen:      make(map[int32]time.Time),
		offsets:       make(map[string]map[int32][]storage.OffsetRow),
		ptp4lInserted: make(chan struct{}),
	}
}

func (s *fakeStorage) GetNodeIDList(ctx context.Context) ([]int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]int32, 0, len(s.nodes))
	for _, id := range s.nodes {
		result = append(result, id)
	}
	return result, nil
}

func (s *fakeStorage) ResolveNodeID(hostname string) (int32, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, ok := s.nodes[hostname]
	return id, ok
}

func (s *fakeStorage) ResolveSensorID(ctx context.Context, nodeID int32, sensor, label string) (int32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := testSensorKey{nodeID: nodeID, sensor: sensor, label: label}
	if id, ok := s.sensors[key]; ok {
		return id, nil
	}

	id := s.nextSensorID
	s.nextSensorID++
	s.sensors[key] = id
	return id, nil
}

func (s *fakeStorage) InsertPtp4l(ctx context.Context, ts time.Time, nodeID int32, offsetNs, frequency, pathDelay int64) error {
	s.mu.Lock()
	s.ptp4lInserts = append(s.ptp4lInserts, ptp4lInsert{
		ts: ts, nodeID: nodeID, offsetNs: offsetNs, frequency: frequency, pathDelay: pathDelay,
	})
	s.mu.Unlock()

	s.ptp4lInsertedOnce.Do(func() {
		close(s.ptp4lInserted)
	})
	return nil
}

func (s *fakeStorage) InsertPhc2sys(ctx context.Context, ts time.Time, nodeID int32, offsetNs, frequency, pathDelay int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.phc2sysInserts = append(s.phc2sysInserts, phc2sysInsert{
		ts: ts, nodeID: nodeID, offsetNs: offsetNs, frequency: frequency, pathDelay: pathDelay,
	})
	return nil
}

func (s *fakeStorage) InsertPps(ctx context.Context, ts time.Time, nodeID int32, offsetNs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ppsInserts = append(s.ppsInserts, ppsInsert{ts: ts, nodeID: nodeID, offsetNs: offsetNs})
	return nil
}

func (s *fakeStorage) InsertNetwork(ctx context.Context, ts time.Time, nodeID int32, rxPackets, txPackets, rxDropped, txDropped, rxErrors, txErrors, collisions int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.networkInserts = append(s.networkInserts, networkInsert{
		ts: ts, nodeID: nodeID, rxPackets: rxPackets, txPackets: txPackets,
		rxDropped: rxDropped, txDropped: txDropped, rxErrors: rxErrors,
		txErrors: txErrors, collisions: collisions,
	})
	return nil
}

func (s *fakeStorage) InsertCpu(ctx context.Context, ts time.Time, nodeID int32, usagePercent float64, ctxSwitches, interrupts, softirqs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cpuInserts = append(s.cpuInserts, cpuInsert{
		ts: ts, nodeID: nodeID, usagePercent: usagePercent,
		ctxSwitches: ctxSwitches, interrupts: interrupts, softirqs: softirqs,
	})
	return nil
}

func (s *fakeStorage) InsertMemory(ctx context.Context, ts time.Time, nodeID int32, memAvailableKb float64, memFreeKb, swapTotalKb, swapFreeKb, buffersKb int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.memoryInserts = append(s.memoryInserts, memoryInsert{
		ts: ts, nodeID: nodeID, memAvailableKb: memAvailableKb,
		memFreeKb: memFreeKb, swapTotalKb: swapTotalKb,
		swapFreeKb: swapFreeKb, buffersKb: buffersKb,
	})
	return nil
}

func (s *fakeStorage) InsertTemperature(ctx context.Context, ts time.Time, sensorID int32, temperature int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.temperatureInserts = append(s.temperatureInserts, temperatureInsert{
		ts: ts, sensorID: sensorID, temperature: temperature,
	})
	return nil
}

func (s *fakeStorage) UpdateNodeInfo(ctx context.Context, hostname string, role string, netInterface string, adapterName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nodeInfoUpdates = append(s.nodeInfoUpdates, nodeInfoUpdate{
		hostname: hostname, role: role, netInterface: netInterface, adapterName: adapterName,
	})
	return nil
}

func (s *fakeStorage) InsertNode(ctx context.Context, hostname string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.nodes[hostname]; ok {
		return nil
	}

	id := s.nextNodeID
	s.nextNodeID++
	s.nodes[hostname] = id
	return nil
}

func (s *fakeStorage) InsertPtp4lPortEvent(ctx context.Context, ts time.Time, nodeID int32, portNum int32, portName string, fromState, toState, eventTrigger string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.portEventInserts = append(s.portEventInserts, portEventInsert{
		ts: ts, nodeID: nodeID, portNum: portNum, portName: portName,
		fromState: fromState, toState: toState, eventTrigger: eventTrigger,
	})
	return nil
}

func (s *fakeStorage) GetOffsets(ctx context.Context, table string, nodeID int32, period time.Duration) ([]storage.OffsetRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	nodeOffsets := s.offsets[table]
	if nodeOffsets == nil {
		return nil, nil
	}
	result := append([]storage.OffsetRow(nil), nodeOffsets[nodeID]...)
	return result, nil
}

func (s *fakeStorage) InsertSlideMetrics(ctx context.Context, table string, ts time.Time, nodeID int32, windowSize int, mtie int64, tdev float64, adev float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.slideMetricsInserts = append(s.slideMetricsInserts, slideMetricsInsert{
		table: table, ts: ts, nodeID: nodeID, windowSize: windowSize,
		mtie: mtie, tdev: tdev, adev: adev,
	})
	return nil
}

func (s *fakeStorage) TouchNode(nodeID int32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastSeen[nodeID] = time.Now()
}

func (s *fakeStorage) DeactivateStaleNodes(ctx context.Context, timeout time.Duration) error {
	return nil
}

func (s *fakeStorage) Close() {
}

type scriptedAdapter struct {
	mu       sync.Mutex
	messages [][]byte
	unblock  <-chan struct{}
	closed   bool
}

func (a *scriptedAdapter) Read() ([]byte, error) {
	a.mu.Lock()
	if len(a.messages) > 0 {
		msg := a.messages[0]
		a.messages = a.messages[1:]
		a.mu.Unlock()
		return msg, nil
	}
	a.mu.Unlock()

	<-a.unblock
	return nil, errors.New("adapter stopped")
}

func (a *scriptedAdapter) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.closed = true
}

func newTestApp(store *fakeStorage, adapter *scriptedAdapter) *App {
	logger := zap.NewNop()
	slider := NewMetricsWindowSlider(store, logger, time.Hour, time.Hour)

	return &App{
		adapter:             adapter,
		storage:             store,
		windowMetricsSlider: *slider,
		logger:              logger,
		cfg: config.Config{
			Worker: config.WorkerConfig{
				NumWorkers: 1,
				QueueSize:  8,
			},
			Slider: config.SliderConfig{
				CalculateInterval:   time.Hour,
				ObservationInterval: time.Hour,
			},
			NodeTimeout: time.Hour,
		},
		msgCh: make(chan *pb.MetricsWrapper, 8),
	}
}

func TestCalculateMetrics(t *testing.T) {
	samples := []Sample{
		{TimeSec: 0, OffsetNs: 0},
		{TimeSec: 1, OffsetNs: 10},
		{TimeSec: 2, OffsetNs: 0},
	}

	if got := CalculateMTIE(samples, 1); got != 10 {
		t.Fatalf("CalculateMTIE()=%d, want 10", got)
	}

	if got := CalculateTDEV(samples, 1); math.Abs(got-8.16496580927726) > 1e-12 {
		t.Fatalf("CalculateTDEV()=%f, want 8.16496580927726", got)
	}

	if got := CalculateADEV(samples, 1, 1); math.Abs(got-1.414213562373095e-8) > 1e-18 {
		t.Fatalf("CalculateADEV()=%g, want 1.414213562373095e-8", got)
	}
}

func TestHandleSystemMessageInsertsAllSystemMetrics(t *testing.T) {
	store := newFakeStorage()
	app := newTestApp(store, nil)
	ts := time.Unix(1700000000, 123)

	app.handleMsg(context.Background(), &pb.MetricsWrapper{
		Type:      pb.MessageType_MESSAGE_TYPE_SYSTEM,
		Timestamp: timestamppb.New(ts),
		NodeName:  "node-a",
		Payload: &pb.MetricsWrapper_System{
			System: &pb.SystemMetrics{
				NetworkStats: &pb.NetworkMetrics{
					RxPackets: 1, TxPackets: 2, RxDropped: 3, TxDropped: 4,
					RxErrors: 5, TxErrors: 6, Collisions: 7,
				},
				CpuStats: &pb.CpuMetrics{
					UsagePercent: 12.5, ContextSwitches: 10, Interrupts: 20, Softirqs: 30,
				},
				MemoryStats: &pb.MemoryMetrics{
					MemAvailableKb: 100, MemFreeKb: 90, SwapTotalKb: 80, SwapFreeKb: 70, BuffersKb: 60,
				},
				TemperatureStats: &pb.TemperatureMetrics{
					ZonesReadings: []*pb.TemperatureMetrics_TemperatureMetric{
						{Sensor: "coretemp", Label: "package", Temperature: 42000},
						{Sensor: "nvme", Label: "composite", Temperature: 37000},
					},
				},
			},
		},
	})

	store.mu.Lock()
	defer store.mu.Unlock()

	if got := store.nodes["node-a"]; got != 1 {
		t.Fatalf("node id=%d, want 1", got)
	}
	if _, ok := store.lastSeen[1]; !ok {
		t.Fatalf("node was not touched")
	}
	if len(store.networkInserts) != 1 {
		t.Fatalf("network inserts=%d, want 1", len(store.networkInserts))
	}
	if store.networkInserts[0].collisions != 7 {
		t.Fatalf("network collisions=%d, want 7", store.networkInserts[0].collisions)
	}
	if len(store.cpuInserts) != 1 {
		t.Fatalf("cpu inserts=%d, want 1", len(store.cpuInserts))
	}
	if store.cpuInserts[0].usagePercent != 12.5 {
		t.Fatalf("cpu usage=%f, want 12.5", store.cpuInserts[0].usagePercent)
	}
	if len(store.memoryInserts) != 1 {
		t.Fatalf("memory inserts=%d, want 1", len(store.memoryInserts))
	}
	if store.memoryInserts[0].memAvailableKb != 100 {
		t.Fatalf("memory available=%f, want 100", store.memoryInserts[0].memAvailableKb)
	}
	if len(store.temperatureInserts) != 2 {
		t.Fatalf("temperature inserts=%d, want 2", len(store.temperatureInserts))
	}
	if store.temperatureInserts[0].sensorID != 1 || store.temperatureInserts[0].temperature != 42000 {
		t.Fatalf("first temperature insert=%+v, want sensor 1 temperature 42000", store.temperatureInserts[0])
	}
}

func TestHandlePtp4lPortEventUpdatesNodeInfoForPhysicalInterface(t *testing.T) {
	store := newFakeStorage()
	app := newTestApp(store, nil)
	ts := time.Unix(1700000100, 0)

	app.handleMsg(context.Background(), &pb.MetricsWrapper{
		Type:      pb.MessageType_MESSAGE_TYPE_PTP4L_PORT_EVENT,
		Timestamp: timestamppb.New(ts),
		NodeName:  "node-b",
		Payload: &pb.MetricsWrapper_Ptp4LPortEvent{
			Ptp4LPortEvent: &pb.Ptp4LPortEvent{
				Port: 1, Interface: "eth0", FromState: "LISTENING",
				ToState: "MASTER", EventTrigger: "RS_MASTER", AdapterName: "i210",
			},
		},
	})

	store.mu.Lock()
	defer store.mu.Unlock()

	if len(store.nodeInfoUpdates) != 1 {
		t.Fatalf("node info updates=%d, want 1", len(store.nodeInfoUpdates))
	}
	update := store.nodeInfoUpdates[0]
	if update.hostname != "node-b" || update.role != "master" || update.netInterface != "eth0" || update.adapterName != "i210" {
		t.Fatalf("node info update=%+v, want node-b/master/eth0/i210", update)
	}
	if len(store.portEventInserts) != 1 {
		t.Fatalf("port event inserts=%d, want 1", len(store.portEventInserts))
	}
	if store.portEventInserts[0].toState != "MASTER" {
		t.Fatalf("port event toState=%q, want MASTER", store.portEventInserts[0].toState)
	}
}

func TestRunDecodesProtoAndWorkerStoresMetric(t *testing.T) {
	store := newFakeStorage()
	stopReads := make(chan struct{})
	ts := time.Unix(1700000200, 456)
	data, err := proto.Marshal(&pb.MetricsWrapper{
		Type:      pb.MessageType_MESSAGE_TYPE_PTP4L,
		Timestamp: timestamppb.New(ts),
		NodeName:  "node-c",
		Payload: &pb.MetricsWrapper_Ptp4L{
			Ptp4L: &pb.Ptp4LMetrics{
				OffsetNs:  -11,
				Frequency: 22,
				PathDelay: 33,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal wrapper: %v", err)
	}

	adapter := &scriptedAdapter{messages: [][]byte{data}, unblock: stopReads}
	app := newTestApp(store, adapter)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx)
	}()

	select {
	case <-store.ptp4lInserted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ptp4l insert")
	}

	cancel()
	close(stopReads)

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error=%v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Run to stop")
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if len(store.ptp4lInserts) != 1 {
		t.Fatalf("ptp4l inserts=%d, want 1", len(store.ptp4lInserts))
	}
	insert := store.ptp4lInserts[0]
	if insert.nodeID != 1 || insert.offsetNs != -11 || insert.frequency != 22 || insert.pathDelay != 33 {
		t.Fatalf("ptp4l insert=%+v, want node 1 offset -11 frequency 22 pathDelay 33", insert)
	}
	if !adapter.closed {
		t.Fatalf("adapter was not closed")
	}
}
