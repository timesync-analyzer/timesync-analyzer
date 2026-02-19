package config

import (
	"fmt"
	"os"
	"time"

	"github.com/ilyakaznacheev/cleanenv"
)

type Config struct {
	Env         string        `yaml:"env"`
	Zmq         ZMQConfig     `yaml:"zmq"`
	DB          DBConfig      `yaml:"db"`
	NodeTimeout time.Duration `yaml:"node_timeout"`
}

type DBConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
}

func (d DBConfig) ConnString() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		d.User, d.Password, d.Host, d.Port, d.DBName, d.SSLMode,
	)
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