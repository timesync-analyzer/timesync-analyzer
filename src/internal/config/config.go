package config

import (
	"fmt"
	"os"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
)

type Config struct {
	Env         string        `env:"APP_ENV"       env-default:"local"`
	Zmq         ZMQConfig     `yaml:"zmq"`
	DB          DBConfig      `yaml:"storage"`
	NodeTimeout time.Duration `yaml:"node_timeout" env-default:"30s"`
	Slider      SliderConfig  `yaml:"slider"`
	Worker      WorkerConfig  `yaml:"worker"`
	Reportd     ReportdConfig `yaml:"reportd"`
}

type WorkerConfig struct {
	NumWorkers int `yaml:"num_workers" env-default:"4"`
	QueueSize  int `yaml:"queue_size"  env-default:"1000"`
}

type SliderConfig struct {
	CalculateInterval   time.Duration `yaml:"calculate_interval"`
	ObservationInterval time.Duration `yaml:"observation_interval"`
}

type DBConfig struct {
	Host     string      `env:"DB_HOST"     env-required:"true"`
	Port     int         `env:"DB_PORT"     env-default:"5432"`
	User     string      `env:"APP_USER"    env-required:"true"`
	Password string      `env:"APP_PASSWORD" env-required:"true"`
	DBName   string      `env:"POSTGRES_DB" env-required:"true"`
	SSLMode  string      `env:"DB_SSLMODE"  env-default:"disable"`
	Batch    BatchConfig `yaml:"batch"`
}

type BatchConfig struct {
	MaxSize       int           `yaml:"max_size"`
	FlushInterval time.Duration `yaml:"flush_interval"`
}

func (d DBConfig) ConnString() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		d.User, d.Password, d.Host, d.Port, d.DBName, d.SSLMode,
	)
}

type ZMQConfig struct {
	Address string        `env:"ZMQ_ADDRESS" env-default:"tcp://*:10000"`
	Timeout time.Duration `yaml:"timeout" env-default:"490ms"`
}

type ReportdConfig struct {
	Listen        string                  `env:"REPORTD_LISTEN"        env-default:":8080"`
	OutDir        string                  `env:"REPORTD_OUT_DIR"       env-default:"reports"`
	Token         string                  `env:"REPORT_TOKEN"`
	JobTimeout    time.Duration           `yaml:"job_timeout" env-default:"15m"`
	QueueSize     int                     `yaml:"queue_size" env-default:"100"`
	WorkerCount   int                     `yaml:"workers" env-default:"1"`
	Dashboards    ReportdDashboardsConfig `yaml:"dashboards"`
	Defaults      ReportdDefaultsConfig   `yaml:"defaults"`
	Alerts        ReportdAlertsConfig     `yaml:"alerts"`
	Grafana       ReportdGrafanaConfig    `yaml:"grafana"`
	Cleanup       ReportdCleanupConfig    `yaml:"cleanup"`
	Auto          ReportdAutoConfig       `yaml:"auto"`
}

type ReportdDashboardsConfig struct {
	Sync    string `yaml:"sync"    env-default:"grafana/dashboards/sync_analysys.json"`
	Network string `yaml:"network" env-default:"grafana/dashboards/network.json"`
	System  string `yaml:"system"  env-default:"grafana/dashboards/system_resources.json"`
}

type ReportdDefaultsConfig struct {
	Groups        []string      `yaml:"groups" env-separator:"," env-default:"offset,status"`
	Period        time.Duration `yaml:"period" env-default:"1h"`
	ChartsPerPage int           `yaml:"charts_per_page" env-default:"3"`
	SkipCharts    bool          `yaml:"skip_charts" env-default:"false"`
}

type ReportdAlertsConfig struct {
	Groups   []string      `yaml:"groups" env-default:"all"`
	Period   time.Duration `yaml:"period" env-default:"1h"`
	Delay    time.Duration `yaml:"delay" env-default:"0s"`
	Cooldown time.Duration `yaml:"cooldown" env-default:"30m"`
}

type ReportdGrafanaConfig struct {
	URL           string        `yaml:"url"            env:"GRAFANA_URL"           env-default:"http://localhost:3000"`
	User          string        `env:"GRAFANA_ADMIN_USER"`
	Password      string        `env:"GRAFANA_ADMIN_PASSWORD"`
	Token         string        `env:"GRAFANA_TOKEN"`
	Node          string        `yaml:"node" env-default:".*"`
	Width         int           `yaml:"width" env-default:"1200"`
	Height        int           `yaml:"height" env-default:"420"`
	RenderWorkers int           `yaml:"render_workers" env-default:"4"`
	RenderTimeout time.Duration `yaml:"render_timeout" env-default:"2m"`
}

type ReportdCleanupConfig struct {
	ReportTTL time.Duration `yaml:"report_ttl" env-default:"168h"`
	Interval  time.Duration `yaml:"interval" env-default:"1h"`
}

type ReportdAutoConfig struct {
	Interval time.Duration `yaml:"interval" env-default:"0s"`
	Window   time.Duration `yaml:"window" env-default:"0s"`
}

func MustLoad(path string) Config {
	if path == "" {
		panic("path to config is empty")
	}

	if _, err := os.Stat(path); err != nil {
		panic(fmt.Sprintf("config with path \"%s\" doesn't exist", path))
	}

	var cfg Config

	if err := cleanenv.ReadConfig(path, &cfg); err != nil {
		panic("config path is empty: " + err.Error())
	}

	return cfg
}
