package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// Flat fields for backward compatibility
	DBURL             string
	RedisAddr         string
	RedisPassword     string
	Port              string
	BankPrivateKeyPEM string
	BankPublicKeyPEM  string

	// Nested fields mirroring YAML structures
	Server        ServerConfig        `yaml:"server"`
	Database      DatabaseConfig      `yaml:"database"`
	Redis         RedisConfig         `yaml:"redis"`
	Observability ObservabilityConfig `yaml:"observability"`
	Security      SecurityConfig      `yaml:"security"`
}

type ServerConfig struct {
	Port string `yaml:"port"`
	Env  string `yaml:"env"`
}

type DatabaseConfig struct {
	URL             string        `yaml:"url"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
}

type ObservabilityConfig struct {
	Tracing TracingConfig `yaml:"tracing"`
	Metrics MetricsConfig `yaml:"metrics"`
}

type TracingConfig struct {
	Enabled      bool   `yaml:"enabled"`
	CollectorURL string `yaml:"collector_url"`
	ServiceName  string `yaml:"service_name"`
}

type MetricsConfig struct {
	Port string `yaml:"port"`
}

type SecurityConfig struct {
	BankPrivateKeyPEM string           `yaml:"bank_private_key"`
	BankPublicKeyPEM  string           `yaml:"bank_public_key"`
	RateLimits        RateLimitsConfig `yaml:"rate_limits"`
}

type RateLimitsConfig struct {
	Anonymous     int `yaml:"anonymous"`
	Authenticated int `yaml:"authenticated"`
	Internal      int `yaml:"internal"`
}

// Temporary struct to parse duration strings from YAML
type rawDatabaseConfig struct {
	URL             string `yaml:"url"`
	MaxOpenConns    int    `yaml:"max_open_conns"`
	MaxIdleConns    int    `yaml:"max_idle_conns"`
	ConnMaxLifetime string `yaml:"conn_max_lifetime"`
}

type rawConfig struct {
	Server        ServerConfig        `yaml:"server"`
	Database      rawDatabaseConfig   `yaml:"database"`
	Redis         RedisConfig         `yaml:"redis"`
	Observability ObservabilityConfig `yaml:"observability"`
	Security      SecurityConfig      `yaml:"security"`
}

func LoadConfig() *Config {
	env := os.Getenv("APP_ENV")
	if env == "" {
		env = "development"
	}

	cfg := &Config{}
	
	// Locate config file
	configPath := findConfigFile(env)
	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err == nil {
			var raw rawConfig
			if errUnmarshal := yaml.Unmarshal(data, &raw); errUnmarshal == nil {
				cfg.Server = raw.Server
				cfg.Redis = raw.Redis
				cfg.Observability = raw.Observability
				cfg.Security = raw.Security

				cfg.Database.URL = raw.Database.URL
				cfg.Database.MaxOpenConns = raw.Database.MaxOpenConns
				cfg.Database.MaxIdleConns = raw.Database.MaxIdleConns
				
				dur, errDur := time.ParseDuration(raw.Database.ConnMaxLifetime)
				if errDur == nil {
					cfg.Database.ConnMaxLifetime = dur
				} else {
					cfg.Database.ConnMaxLifetime = 5 * time.Minute
				}
			}
		}
	}

	// Apply defaults if YAML loading failed or fields are missing
	if cfg.Server.Port == "" {
		cfg.Server.Port = "8080"
	}
	if cfg.Server.Env == "" {
		cfg.Server.Env = env
	}
	if cfg.Database.URL == "" {
		cfg.Database.URL = "postgres://postgres:postgres@localhost:5432/offlinepay?sslmode=disable"
	}
	if cfg.Database.MaxOpenConns == 0 {
		cfg.Database.MaxOpenConns = 25
	}
	if cfg.Database.MaxIdleConns == 0 {
		cfg.Database.MaxIdleConns = 5
	}
	if cfg.Database.ConnMaxLifetime == 0 {
		cfg.Database.ConnMaxLifetime = 5 * time.Minute
	}
	if cfg.Redis.Addr == "" {
		cfg.Redis.Addr = "localhost:6379"
	}
	if cfg.Security.RateLimits.Anonymous == 0 {
		cfg.Security.RateLimits.Anonymous = 20
	}
	if cfg.Security.RateLimits.Authenticated == 0 {
		cfg.Security.RateLimits.Authenticated = 100
	}
	if cfg.Security.RateLimits.Internal == 0 {
		cfg.Security.RateLimits.Internal = 1000
	}

	// Environment variable overrides
	if dbURL := os.Getenv("DATABASE_URL"); dbURL != "" {
		cfg.Database.URL = dbURL
	}
	if redisAddr := os.Getenv("REDIS_ADDR"); redisAddr != "" {
		cfg.Redis.Addr = redisAddr
	}
	if redisPassword := os.Getenv("REDIS_PASSWORD"); redisPassword != "" {
		cfg.Redis.Password = redisPassword
	}
	if port := os.Getenv("PORT"); port != "" {
		cfg.Server.Port = port
	}
	if bankPriv := os.Getenv("BANK_PRIVATE_KEY"); bankPriv != "" {
		cfg.Security.BankPrivateKeyPEM = bankPriv
	}
	if bankPub := os.Getenv("BANK_PUBLIC_KEY"); bankPub != "" {
		cfg.Security.BankPublicKeyPEM = bankPub
	}

	// Map to flat fields for backward compatibility
	cfg.DBURL = cfg.Database.URL
	cfg.RedisAddr = cfg.Redis.Addr
	cfg.RedisPassword = cfg.Redis.Password
	cfg.Port = cfg.Server.Port
	cfg.BankPrivateKeyPEM = cfg.Security.BankPrivateKeyPEM
	cfg.BankPublicKeyPEM = cfg.Security.BankPublicKeyPEM

	return cfg
}

func findConfigFile(env string) string {
	fileName := fmt.Sprintf("%s.yaml", env)
	
	// Traversal list to walk up directories to find "config" folder
	dirsToTry := []string{
		"config",
		"../config",
		"../../config",
		"../../../config",
		"../../../../config",
	}

	for _, dir := range dirsToTry {
		path := filepath.Join(dir, fileName)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}
