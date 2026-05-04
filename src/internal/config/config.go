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
	NodeTimeout time.Duration `yaml:"node_timeout" env:"NODE_TIMEOUT" env-default:"30s"`
	Slider      SliderConfig  `yaml:"slider"`
	Worker      WorkerConfig  `yaml:"worker"`
}

type WorkerConfig struct {
	NumWorkers int `yaml:"num_workers" env:"WORKER_NUM_WORKERS" env-default:"4"`
	QueueSize  int `yaml:"queue_size"  env:"WORKER_QUEUE_SIZE"  env-default:"1000"`
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
	Timeout time.Duration `yaml:"timeout" env:"ZMQ_TIMEOUT" env-default:"490ms"`
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
