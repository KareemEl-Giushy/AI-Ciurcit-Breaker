package utils

import (
	"os"
	"strconv"
	"time"
)

// GetEnv retrieves the environment variable for key, or fallback if empty.
func GetEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return fallback
}

// GetEnvDuration parses a duration environment variable, or fallback if invalid or empty.
func GetEnvDuration(key string, fallback time.Duration) time.Duration {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return fallback
}

// GetEnvInt parses an integer environment variable, or fallback if invalid or empty.
func GetEnvInt(key string, fallback int) int {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			return n
		}
	}
	return fallback
}

// GetEnvFloat parses a float64 environment variable, or fallback if invalid or empty.
func GetEnvFloat(key string, fallback float64) float64 {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return fallback
}

// GetEnvBool parses a boolean environment variable, or fallback if invalid or empty.
func GetEnvBool(key string, fallback bool) bool {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			return b
		}
	}
	return fallback
}
