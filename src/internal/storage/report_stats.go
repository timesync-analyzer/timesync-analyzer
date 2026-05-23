package storage

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (s *PostgresStorage) LoadReportStats(ctx context.Context, from, to time.Time) ([]ReportStats, error) {
	return LoadReportStats(ctx, s.pool, from, to)
}

func LoadReportStats(ctx context.Context, pool *pgxpool.Pool, from, to time.Time) ([]ReportStats, error) {
	const query = `
WITH expected(metric, unit, protocol, metric_order, protocol_order) AS (
	VALUES
		('offset'::text, 'ns'::text, 'ptp4l'::text, 1, 1),
		('offset'::text, 'ns'::text, 'phc2sys'::text, 1, 2),
		('offset'::text, 'ns'::text, 'pps'::text, 1, 3),
		('frequency'::text, ''::text, 'ptp4l'::text, 2, 1),
		('frequency'::text, ''::text, 'phc2sys'::text, 2, 2),
		('path_delay'::text, 'ns'::text, 'ptp4l'::text, 3, 1),
		('path_delay'::text, 'ns'::text, 'phc2sys'::text, 3, 2)
),
samples AS (
	SELECT 'offset'::text AS metric, 'ptp4l'::text AS protocol, node_id, offset_ns::double precision AS value
	FROM timesync.ptp4l_metrics
	WHERE time >= $1 AND time < $2 AND offset_ns IS NOT NULL

	UNION ALL

	SELECT 'offset'::text AS metric, 'phc2sys'::text AS protocol, node_id, offset_ns::double precision AS value
	FROM timesync.phc2sys_metrics
	WHERE time >= $1 AND time < $2 AND offset_ns IS NOT NULL

	UNION ALL

	SELECT 'offset'::text AS metric, 'pps'::text AS protocol, node_id, offset_ns::double precision AS value
	FROM timesync.pps_metrics
	WHERE time >= $1 AND time < $2 AND offset_ns IS NOT NULL

	UNION ALL

	SELECT 'frequency'::text AS metric, 'ptp4l'::text AS protocol, node_id, frequency::double precision AS value
	FROM timesync.ptp4l_metrics
	WHERE time >= $1 AND time < $2 AND frequency IS NOT NULL

	UNION ALL

	SELECT 'frequency'::text AS metric, 'phc2sys'::text AS protocol, node_id, frequency::double precision AS value
	FROM timesync.phc2sys_metrics
	WHERE time >= $1 AND time < $2 AND frequency IS NOT NULL

	UNION ALL

	SELECT 'path_delay'::text AS metric, 'ptp4l'::text AS protocol, node_id, path_delay::double precision AS value
	FROM timesync.ptp4l_metrics
	WHERE time >= $1 AND time < $2 AND path_delay IS NOT NULL

	UNION ALL

	SELECT 'path_delay'::text AS metric, 'phc2sys'::text AS protocol, node_id, path_delay::double precision AS value
	FROM timesync.phc2sys_metrics
	WHERE time >= $1 AND time < $2 AND path_delay IS NOT NULL
),
agg AS (
	SELECT
		metric,
		protocol,
		node_id,
		count(*)::bigint AS samples,
		min(value) AS min_value,
		max(value) AS max_value,
		avg(value) AS mean_value,
		sqrt(avg(value * value)) AS rms_value
	FROM samples
	GROUP BY metric, protocol, node_id
)
SELECT
	n.hostname,
	e.protocol,
	e.metric,
	e.unit,
	COALESCE(a.samples, 0) AS samples,
	a.min_value,
	a.max_value,
	a.mean_value,
	a.rms_value
FROM timesync.nodes n
CROSS JOIN expected e
LEFT JOIN agg a ON a.node_id = n.node_id AND a.metric = e.metric AND a.protocol = e.protocol
ORDER BY e.metric_order, e.protocol_order, lower(n.hostname), n.hostname`

	rows, err := pool.Query(ctx, query, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ReportStats
	for rows.Next() {
		var item ReportStats
		var minValue, maxValue, meanValue, rmsValue pgtype.Float8
		if err := rows.Scan(&item.Hostname, &item.Protocol, &item.Metric, &item.Unit, &item.Samples, &minValue, &maxValue, &meanValue, &rmsValue); err != nil {
			return nil, err
		}
		item.HasData = item.Samples > 0
		if minValue.Valid {
			item.Min = minValue.Float64
		}
		if maxValue.Valid {
			item.Max = maxValue.Float64
		}
		if meanValue.Valid {
			item.Mean = meanValue.Float64
		}
		if rmsValue.Valid {
			item.RMS = rmsValue.Float64
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
