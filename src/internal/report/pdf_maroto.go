package report

import (
	"fmt"
	"time"

	"timesync-analyzer/src/internal/storage"

	"github.com/johnfercher/maroto/v2"
	"github.com/johnfercher/maroto/v2/pkg/components/col"
	marotoimage "github.com/johnfercher/maroto/v2/pkg/components/image"
	"github.com/johnfercher/maroto/v2/pkg/components/page"
	"github.com/johnfercher/maroto/v2/pkg/components/row"
	"github.com/johnfercher/maroto/v2/pkg/components/text"
	"github.com/johnfercher/maroto/v2/pkg/config"
	"github.com/johnfercher/maroto/v2/pkg/consts/align"
	"github.com/johnfercher/maroto/v2/pkg/consts/extension"
	"github.com/johnfercher/maroto/v2/pkg/consts/fontstyle"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/johnfercher/maroto/v2/pkg/props"
)

type MarotoPDFRenderer struct{}

func (MarotoPDFRenderer) WritePDF(path string, from, to time.Time, stats []storage.ReportStats, charts []pdfChart, chartsPerPage int) error {
	cfg := config.NewBuilder().
		WithLeftMargin(8).
		WithRightMargin(8).
		WithTopMargin(10).
		WithBottomMargin(10).
		WithCompression(true).
		Build()

	m := maroto.New(cfg)
	m.AddRows(
		text.NewRow(8, "Time Sync Summary", props.Text{Size: 16, Style: fontstyle.Bold}),
		text.NewRow(5, reportPeriodHeader(from, to), props.Text{Size: 9}),
		text.NewRow(5, fmt.Sprintf("Generated: %s", reportDisplayTime(time.Now())), props.Text{Size: 9}),
		row.New(3),
	)

	addMarotoStatsSection(m, "Offset Statistics (ns)", "offset", stats)
	addMarotoStatsSection(m, "Frequency Statistics", "frequency", stats)
	addMarotoStatsSection(m, "Path Delay Statistics (ns)", "path_delay", stats)
	addMarotoChartPages(m, charts, chartsPerPage)

	document, err := m.Generate()
	if err != nil {
		return err
	}
	return document.Save(path)
}

func addMarotoStatsSection(m core.Maroto, title, metric string, stats []storage.ReportStats) {
	m.AddRows(
		row.New(3),
		text.NewRow(6, title, props.Text{Size: 11, Style: fontstyle.Bold}),
	)
	m.AddRow(5,
		marotoTextCol(3, "host", marotoHeaderText()),
		marotoTextCol(1, "proto", marotoHeaderText()),
		marotoTextCol(2, "samples", marotoHeaderTextRight()),
		marotoTextCol(1, "min", marotoHeaderTextRight()),
		marotoTextCol(1, "max", marotoHeaderTextRight()),
		marotoTextCol(2, "mean", marotoHeaderTextRight()),
		marotoTextCol(2, "rms", marotoHeaderTextRight()),
	)

	for _, item := range stats {
		if item.Metric != metric {
			continue
		}
		m.AddRow(4.5,
			marotoTextCol(3, truncate(item.Hostname, 28), marotoBodyText()),
			marotoTextCol(1, item.Protocol, marotoBodyText()),
			marotoTextCol(2, fmt.Sprintf("%d", item.Samples), marotoBodyTextRight()),
			marotoTextCol(1, formatPDFMetric(item, item.Min), marotoBodyTextRight()),
			marotoTextCol(1, formatPDFMetric(item, item.Max), marotoBodyTextRight()),
			marotoTextCol(2, formatPDFMetric(item, item.Mean), marotoBodyTextRight()),
			marotoTextCol(2, formatPDFMetric(item, item.RMS), marotoBodyTextRight()),
		)
	}
}

func addMarotoChartPages(m core.Maroto, charts []pdfChart, chartsPerPage int) {
	if len(charts) == 0 {
		return
	}
	if chartsPerPage <= 0 {
		chartsPerPage = 1
	}
	if chartsPerPage > 4 {
		chartsPerPage = 4
	}

	imageHeight := 230.0/float64(chartsPerPage) - 7
	if imageHeight < 42 {
		imageHeight = 42
	}
	if imageHeight > 74 {
		imageHeight = 74
	}

	for start := 0; start < len(charts); start += chartsPerPage {
		end := start + chartsPerPage
		if end > len(charts) {
			end = len(charts)
		}

		var rows []core.Row
		for _, chart := range charts[start:end] {
			if len(chart.PNG) == 0 {
				continue
			}
			rows = append(rows,
				text.NewRow(5, truncate(chart.Title, 120), props.Text{Size: 9, Style: fontstyle.Bold}),
				marotoimage.NewFromBytesRow(imageHeight, chart.PNG, extension.Png, props.Rect{
					Center:             true,
					Percent:            95,
					JustReferenceWidth: true,
				}),
			)
		}
		if len(rows) > 0 {
			m.AddPages(page.New().Add(rows...))
		}
	}
}

func marotoTextCol(size int, value string, prop props.Text) core.Col {
	return col.New(size).Add(text.New(value, prop))
}

func marotoHeaderText() props.Text {
	return props.Text{
		Size:  7,
		Style: fontstyle.Bold,
		Top:   1,
		Left:  1,
		Right: 1,
	}
}

func marotoHeaderTextRight() props.Text {
	prop := marotoHeaderText()
	prop.Align = align.Right
	return prop
}

func marotoBodyText() props.Text {
	return props.Text{
		Size:  7,
		Top:   1,
		Left:  1,
		Right: 1,
	}
}

func marotoBodyTextRight() props.Text {
	prop := marotoBodyText()
	prop.Align = align.Right
	return prop
}
