package config

import (
	"fmt"
	"os"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
)

type Config struct {
	Env string `yaml:"env"`
	Zmq ZMQConfig `yaml:"zmq"`
}

type ZMQConfig struct {
	Address string `yaml:"address"`
	Timeout time.Duration `yaml:"timeout"`
}

func MustLoad(path string) (Config) {
	if path == "" {
		panic("path to config is empty")
	}

	if _, err := os.Stat(path); err != nil {
		panic(fmt.Sprintf("config with path \"%s\" doesn't exist", path));
	}

	var cfg Config

	if err := cleanenv.ReadConfig(path, &cfg); err != nil {
		panic("config path is empty: " + err.Error())
	}

	return cfg
}