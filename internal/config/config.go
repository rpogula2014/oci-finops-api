package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr       string
	CHAddr         string
	CHDatabase     string
	CHUsername     string
	CHPassword     string
	CHSecure       bool
	CHDialTimeout  time.Duration
	CHQueryTimeout time.Duration
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	IdleTimeout    time.Duration
}

func Load() (Config, error) {
	c := Config{
		HTTPAddr: env("HTTP_ADDR", ":8080"), CHAddr: env("CLICKHOUSE_ADDR", "localhost:9000"),
		CHDatabase: env("CLICKHOUSE_DATABASE", "default"), CHUsername: env("CLICKHOUSE_USERNAME", "default"),
		CHPassword: os.Getenv("CLICKHOUSE_PASSWORD"),
	}
	var err error
	if c.CHSecure, err = strconv.ParseBool(env("CLICKHOUSE_SECURE", "false")); err != nil {
		return Config{}, fmt.Errorf("CLICKHOUSE_SECURE: %w", err)
	}
	for name, target := range map[string]*time.Duration{"CLICKHOUSE_DIAL_TIMEOUT": &c.CHDialTimeout, "CLICKHOUSE_QUERY_TIMEOUT": &c.CHQueryTimeout, "HTTP_READ_TIMEOUT": &c.ReadTimeout, "HTTP_WRITE_TIMEOUT": &c.WriteTimeout, "HTTP_IDLE_TIMEOUT": &c.IdleTimeout} {
		value := env(name, map[string]string{"CLICKHOUSE_DIAL_TIMEOUT": "5s", "CLICKHOUSE_QUERY_TIMEOUT": "15s", "HTTP_READ_TIMEOUT": "10s", "HTTP_WRITE_TIMEOUT": "30s", "HTTP_IDLE_TIMEOUT": "60s"}[name])
		*target, err = time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	return c, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
