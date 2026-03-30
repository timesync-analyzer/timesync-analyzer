package app

import (
	"container/list"
	"context"
	"math"
	"time"
	"timesync-analyzer/src/internal/storage"

	"go.uber.org/zap"
)

type MetricsWindowSlider struct {
	storage           storage.Storage
	interval          time.Duration
	observationPeriod time.Duration
	logger            *zap.Logger
}

func NewMetricsWindowSlider(
	storage storage.Storage,
	logger *zap.Logger,
	interval time.Duration,
	observationPeriod time.Duration,
) *MetricsWindowSlider {
	return &MetricsWindowSlider{
		storage:           storage,
		logger:            logger,
		interval:          interval,
		observationPeriod: observationPeriod,
	}
}

func (s *MetricsWindowSlider) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.calculateSlideWindows(ctx); err != nil {
				s.logger.Error("slide windows calculation failed", zap.Error(err))
			}
		}
	}
}

var (
	tauSeconds = []int{5, 7, 10, 15, 20, 30, 50, 70, 100, 150, 200, 250, 300, 400, 500, 600, 700, 750, 800, 900, 1000}

	tables = map[string]string{
		"ptp4l_metrics":   "ptp4l_quality_metrics",
		"phc2sys_metrics":  "phc2sys_quality_metrics",
		"pps_metrics":      "pps_quality_metrics",
	}
)

func (s *MetricsWindowSlider) calculateSlideWindows(ctx context.Context) error {
	nodes, err := s.storage.GetNodeIDList(ctx)
	if err != nil {
		return err
	}

	now := time.Now()

	for metricsTable, qualityTable := range tables {
		for _, nodeID := range nodes {
			offsets, err := s.storage.GetOffsets(ctx, metricsTable, nodeID, s.observationPeriod)
			if err != nil {
				s.logger.Error("can't fetch offsets",
					zap.String("table", metricsTable),
					zap.Int32("node_id", nodeID),
					zap.Error(err),
				)
				continue
			}

			if len(offsets) < 3 {
				continue
			}
			samples := toSamples(offsets)
			tau0 := estimateTau0(samples)

			for _, tau := range tauSeconds {
				mtie := CalculateMTIE(samples, float64(tau))

				tauSamples := int(float64(tau) / tau0)
				if tauSamples < 1 {
					tauSamples = 1
				}
				tdev := CalculateTDEV(samples, tauSamples)
				adev := CalculateADEV(samples, tauSamples, float64(tau))

				if err := s.storage.InsertSlideMetrics(
					ctx, qualityTable, now, nodeID, tau, mtie, tdev, adev,
				); err != nil {
					s.logger.Error("can't insert metrics",
						zap.String("table", qualityTable),
						zap.Int32("node_id", nodeID),
						zap.Int("tau", tau),
						zap.Error(err),
					)
				}
			}
		}
	}
	return nil
}

func toSamples(offsets []storage.OffsetRow) []Sample {
	samples := make([]Sample, len(offsets))
	for i, o := range offsets {
		samples[i] = Sample{
			TimeSec:  float64(o.Time.UnixNano()) / 1e9,
			OffsetNs: o.OffsetNs,
		}
	}
	return samples
}

func estimateTau0(samples []Sample) float64 {
	if len(samples) < 2 {
		return 1.0
	}

	diffs := make([]float64, len(samples)-1)
	for i := 1; i < len(samples); i++ {
		diffs[i-1] = samples[i].TimeSec - samples[i-1].TimeSec
	}

	var sum float64
	for _, d := range diffs {
		sum += d
	}
	avg := sum / float64(len(diffs))
	if avg <= 0 {
		return 1.0
	}
	return avg
}

type Sample struct {
	TimeSec  float64
	OffsetNs int64
}

func CalculateMTIE(samples []Sample, tauSeconds float64) int64 {
	n := len(samples)
	if n < 2 {
		return 0
	}

	maxDeque := list.New()
	minDeque := list.New()
	var mtie int64
	left := 0

	for right := 0; right < n; right++ {
		for maxDeque.Len() > 0 {
			idx := maxDeque.Back().Value.(int)
			if samples[idx].OffsetNs <= samples[right].OffsetNs {
				maxDeque.Remove(maxDeque.Back())
			} else {
				break
			}
		}
		maxDeque.PushBack(right)

		for minDeque.Len() > 0 {
			idx := minDeque.Back().Value.(int)
			if samples[idx].OffsetNs >= samples[right].OffsetNs {
				minDeque.Remove(minDeque.Back())
			} else {
				break
			}
		}
		minDeque.PushBack(right)

		for left < right && (samples[right].TimeSec-samples[left].TimeSec) > tauSeconds {
			left++
			for maxDeque.Len() > 0 && maxDeque.Front().Value.(int) < left {
				maxDeque.Remove(maxDeque.Front())
			}
			for minDeque.Len() > 0 && minDeque.Front().Value.(int) < left {
				minDeque.Remove(minDeque.Front())
			}
		}

		if maxDeque.Len() > 0 && minDeque.Len() > 0 {
			maxVal := samples[maxDeque.Front().Value.(int)].OffsetNs
			minVal := samples[minDeque.Front().Value.(int)].OffsetNs
			if diff := maxVal - minVal; diff > mtie {
				mtie = diff
			}
		}
	}

	return mtie
}

func CalculateADEV(samples []Sample, tauSamples int, tauSeconds float64) float64 {
	n := len(samples)
	if n < 2 * tauSamples + 1 {
		return 0
	}

	tauNs := tauSeconds * 1e9
	if tauNs <= 0 {
		return 0
	}

	var sumSq float64
	var count int

	for i := 0; i + 2 * tauSamples < n; i++ {
		diff := float64(samples[i + 2 * tauSamples].OffsetNs) -
			2 * float64(samples[i + tauSamples].OffsetNs) +
			float64(samples[i].OffsetNs)
		sumSq += diff * diff
		count++
	}

	if count == 0 {
		return 0
	}

	return math.Sqrt(sumSq / (2.0 * float64(count) * tauNs * tauNs))
}

func CalculateTDEV(samples []Sample, tauSamples int) float64 {
	n := len(samples)
	if n < 2*tauSamples+1 {
		return 0
	}

	var sumSq float64
	var count int

	for i := 0; i + 2 * tauSamples < n; i++ {
		diff := float64(samples[i + 2 * tauSamples].OffsetNs) -
			2 * float64(samples[i + tauSamples].OffsetNs) +
			float64(samples[i].OffsetNs)
		sumSq += diff * diff
		count++
	}

	if count == 0 {
		return 0
	}

	return math.Sqrt(sumSq / (6.0 * float64(count)))
}