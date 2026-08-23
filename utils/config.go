package utils

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// YAMLConfig represents the raw structure of an optional config.yaml file.
type YAMLConfig struct {
	Server struct {
		Port      string `yaml:"port"`
		TargetURL string `yaml:"target_url"`
		LogLevel  string `yaml:"log_level"`
	} `yaml:"server"`
	SlidingWindow struct {
		WindowDuration string `yaml:"window_duration"`
		MaxRequests    *int   `yaml:"max_requests"`
		MaxTokens      *int   `yaml:"max_tokens"`
		EnforceLimits  *bool  `yaml:"enforce_limits"`
	} `yaml:"sliding_window"`
	CircuitBreaker struct {
		Enabled            *bool    `yaml:"enabled"`
		ClassALimit        *int     `yaml:"class_a_limit"`
		ClassBLimit        *int     `yaml:"class_b_limit"`
		MaxHammingDistance *int     `yaml:"max_hamming_distance"`
		JaccardThreshold   *float64 `yaml:"jaccard_threshold"`
	} `yaml:"circuit_breaker"`
	Velocity struct {
		Enabled            *bool    `yaml:"enabled"`
		MaxRPS             *float64 `yaml:"max_rps"`
		MaxEndpointRepeats *int     `yaml:"max_endpoint_repeats"`
		RepeatWindow       string   `yaml:"repeat_window"`
	} `yaml:"velocity"`
	Storage struct {
		SaveConversations *bool  `yaml:"save_conversations"`
		ConversationsDir  string `yaml:"conversations_dir"`
	} `yaml:"storage"`
}

// AppConfig contains the fully resolved, merged, and validated configuration settings.
type AppConfig struct {
	// Server & Routing
	Port            string
	TargetURL       *url.URL
	TargetURLString string
	LogLevel        slog.Level
	LogLevelString  string

	// Sliding Window
	WindowDuration time.Duration
	MaxRequests    int
	MaxTokens      int
	EnforceLimits  bool

	// Circuit Breaker
	CBEnabled          bool
	CBClassALimit      int
	CBClassBLimit      int
	CBMaxHammingDist   int
	CBJaccardThreshold float64

	// Velocity Detection
	VelocityEnabled            bool
	VelocityMaxRPS             float64
	VelocityMaxEndpointRepeats int
	VelocityRepeatWindow       time.Duration

	// Storage & Persistence
	SaveConversations bool
	SaveDir           string

	// Metadata
	LoadedConfigFile string
}

// LoadYAMLConfigFile reads and unmarshals a YAML configuration file from the specified path.
// Returns an error if the file exists but cannot be read or parsed.
// If the path is empty or the file does not exist, returns (nil, nil).
func LoadYAMLConfigFile(path string) (*YAMLConfig, error) {
	if path == "" {
		return nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read config file %q: %w", path, err)
	}

	var cfg YAMLConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse YAML config file %q: %w", path, err)
	}

	return &cfg, nil
}

// ParseYAMLDuration parses a duration string or falls back to the provided default.
func ParseYAMLDuration(s string, fallback time.Duration) time.Duration {
	if s == "" {
		return fallback
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return fallback
	}
	return d
}

// Validate validates the values inside an AppConfig instance.
func (cfg *AppConfig) Validate() error {
	if cfg.Port == "" {
		return fmt.Errorf("port cannot be empty")
	}

	if cfg.TargetURL == nil || cfg.TargetURL.Scheme == "" || cfg.TargetURL.Host == "" {
		return fmt.Errorf("invalid target URL %q: must include scheme (http/https) and host", cfg.TargetURLString)
	}

	if cfg.WindowDuration <= 0 {
		return fmt.Errorf("window duration must be positive, got %s", cfg.WindowDuration)
	}
	if cfg.MaxRequests < 0 {
		return fmt.Errorf("window max requests cannot be negative, got %d", cfg.MaxRequests)
	}
	if cfg.MaxTokens < 0 {
		return fmt.Errorf("window max tokens cannot be negative, got %d", cfg.MaxTokens)
	}

	if cfg.CBClassALimit <= 0 {
		return fmt.Errorf("circuit breaker class A limit must be >= 1, got %d", cfg.CBClassALimit)
	}
	if cfg.CBClassBLimit <= 0 {
		return fmt.Errorf("circuit breaker class B limit must be >= 1, got %d", cfg.CBClassBLimit)
	}
	if cfg.CBMaxHammingDist < 0 || cfg.CBMaxHammingDist > 64 {
		return fmt.Errorf("circuit breaker max Hamming distance must be between 0 and 64, got %d", cfg.CBMaxHammingDist)
	}
	if cfg.CBJaccardThreshold <= 0.0 || cfg.CBJaccardThreshold > 1.0 {
		return fmt.Errorf("circuit breaker Jaccard threshold must be in range (0.0, 1.0], got %f", cfg.CBJaccardThreshold)
	}

	if cfg.VelocityMaxRPS < 0 {
		return fmt.Errorf("velocity max RPS cannot be negative, got %f", cfg.VelocityMaxRPS)
	}
	if cfg.VelocityMaxEndpointRepeats < 0 {
		return fmt.Errorf("velocity max endpoint repeats cannot be negative, got %d", cfg.VelocityMaxEndpointRepeats)
	}
	if cfg.VelocityRepeatWindow <= 0 {
		return fmt.Errorf("velocity repeat window must be positive, got %s", cfg.VelocityRepeatWindow)
	}

	if cfg.SaveConversations && strings.TrimSpace(cfg.SaveDir) == "" {
		return fmt.Errorf("conversations save directory cannot be empty when save_conversations is enabled")
	}

	return nil
}

// ParseAndValidateConfig parses configuration across all tiers:
// Defaults -> YAML Config File -> Environment Variables -> Command-Line Flags
// and performs full sanity validation.
func ParseAndValidateConfig(args []string) (*AppConfig, error) {
	// First pass: extract -config argument or CONFIG_FILE environment variable
	configPath := GetEnv("CONFIG_FILE", "config.yaml")
	for i, arg := range args {
		if (arg == "-config" || arg == "--config") && i+1 < len(args) {
			configPath = args[i+1]
			break
		}
		if strings.HasPrefix(arg, "-config=") || strings.HasPrefix(arg, "--config=") {
			parts := strings.SplitN(arg, "=", 2)
			configPath = parts[1]
			break
		}
	}

	yamlCfg, err := LoadYAMLConfigFile(configPath)
	if err != nil && configPath != "config.yaml" {
		return nil, fmt.Errorf("failed to load YAML config file: %w", err)
	}

	var loadedConfigFile string
	if yamlCfg != nil {
		loadedConfigFile = configPath
	}

	// 1. Resolve Server & Routing Defaults
	defaultPort := "8080"
	if yamlCfg != nil && yamlCfg.Server.Port != "" {
		defaultPort = yamlCfg.Server.Port
	}
	defaultPort = GetEnv("PORT", defaultPort)

	defaultTarget := "http://localhost:3000"
	if yamlCfg != nil && yamlCfg.Server.TargetURL != "" {
		defaultTarget = yamlCfg.Server.TargetURL
	}
	defaultTarget = GetEnv("TARGET_URL", defaultTarget)

	defaultLogLevel := "INFO"
	if yamlCfg != nil && yamlCfg.Server.LogLevel != "" {
		defaultLogLevel = yamlCfg.Server.LogLevel
	}
	defaultLogLevel = GetEnv("LOG_LEVEL", defaultLogLevel)

	// 2. Resolve Sliding Window Defaults
	defaultWindowDuration := 60 * time.Second
	if yamlCfg != nil && yamlCfg.SlidingWindow.WindowDuration != "" {
		defaultWindowDuration = ParseYAMLDuration(yamlCfg.SlidingWindow.WindowDuration, defaultWindowDuration)
	}
	defaultWindowDuration = GetEnvDuration("WINDOW_DURATION", defaultWindowDuration)

	defaultMaxRequests := 0
	if yamlCfg != nil && yamlCfg.SlidingWindow.MaxRequests != nil {
		defaultMaxRequests = *yamlCfg.SlidingWindow.MaxRequests
	}
	defaultMaxRequests = GetEnvInt("WINDOW_MAX_REQUESTS", defaultMaxRequests)

	defaultMaxTokens := 0
	if yamlCfg != nil && yamlCfg.SlidingWindow.MaxTokens != nil {
		defaultMaxTokens = *yamlCfg.SlidingWindow.MaxTokens
	}
	defaultMaxTokens = GetEnvInt("WINDOW_MAX_TOKENS", defaultMaxTokens)

	defaultEnforceLimits := false
	if yamlCfg != nil && yamlCfg.SlidingWindow.EnforceLimits != nil {
		defaultEnforceLimits = *yamlCfg.SlidingWindow.EnforceLimits
	}
	defaultEnforceLimits = GetEnvBool("ENFORCE_LIMITS", defaultEnforceLimits)

	// 3. Resolve Circuit Breaker Defaults
	defaultCBEnabled := true
	if yamlCfg != nil && yamlCfg.CircuitBreaker.Enabled != nil {
		defaultCBEnabled = *yamlCfg.CircuitBreaker.Enabled
	}
	defaultCBEnabled = GetEnvBool("CB_ENABLED", defaultCBEnabled)

	defaultCBClassA := 3
	if yamlCfg != nil && yamlCfg.CircuitBreaker.ClassALimit != nil {
		defaultCBClassA = *yamlCfg.CircuitBreaker.ClassALimit
	}
	defaultCBClassA = GetEnvInt("CB_CLASS_A_LIMIT", defaultCBClassA)

	defaultCBClassB := 4
	if yamlCfg != nil && yamlCfg.CircuitBreaker.ClassBLimit != nil {
		defaultCBClassB = *yamlCfg.CircuitBreaker.ClassBLimit
	}
	defaultCBClassB = GetEnvInt("CB_CLASS_B_LIMIT", defaultCBClassB)

	defaultCBHamming := 3
	if yamlCfg != nil && yamlCfg.CircuitBreaker.MaxHammingDistance != nil {
		defaultCBHamming = *yamlCfg.CircuitBreaker.MaxHammingDistance
	}
	defaultCBHamming = GetEnvInt("CB_MAX_HAMMING_DIST", defaultCBHamming)

	defaultCBJaccard := 0.85
	if yamlCfg != nil && yamlCfg.CircuitBreaker.JaccardThreshold != nil {
		defaultCBJaccard = *yamlCfg.CircuitBreaker.JaccardThreshold
	}
	defaultCBJaccard = GetEnvFloat("CB_JACCARD_THRESHOLD", defaultCBJaccard)

	// 4. Resolve Velocity Defaults
	defaultVelocityEnabled := true
	if yamlCfg != nil && yamlCfg.Velocity.Enabled != nil {
		defaultVelocityEnabled = *yamlCfg.Velocity.Enabled
	}
	defaultVelocityEnabled = GetEnvBool("VELOCITY_ENABLED", defaultVelocityEnabled)

	defaultVelocityMaxRPS := 5.0
	if yamlCfg != nil && yamlCfg.Velocity.MaxRPS != nil {
		defaultVelocityMaxRPS = *yamlCfg.Velocity.MaxRPS
	}
	defaultVelocityMaxRPS = GetEnvFloat("VELOCITY_MAX_RPS", defaultVelocityMaxRPS)

	defaultVelocityMaxRepeats := 20
	if yamlCfg != nil && yamlCfg.Velocity.MaxEndpointRepeats != nil {
		defaultVelocityMaxRepeats = *yamlCfg.Velocity.MaxEndpointRepeats
	}
	defaultVelocityMaxRepeats = GetEnvInt("VELOCITY_MAX_ENDPOINT_REPEATS", defaultVelocityMaxRepeats)

	defaultVelocityRepeatWindow := 10 * time.Second
	if yamlCfg != nil && yamlCfg.Velocity.RepeatWindow != "" {
		defaultVelocityRepeatWindow = ParseYAMLDuration(yamlCfg.Velocity.RepeatWindow, defaultVelocityRepeatWindow)
	}
	defaultVelocityRepeatWindow = GetEnvDuration("VELOCITY_REPEAT_WINDOW", defaultVelocityRepeatWindow)

	// 5. Resolve Storage Defaults
	defaultSaveConversations := true
	if yamlCfg != nil && yamlCfg.Storage.SaveConversations != nil {
		defaultSaveConversations = *yamlCfg.Storage.SaveConversations
	}
	defaultSaveConversations = GetEnvBool("SAVE_CONVERSATIONS", defaultSaveConversations)

	defaultSaveDir := "./conversations"
	if yamlCfg != nil && yamlCfg.Storage.ConversationsDir != "" {
		defaultSaveDir = yamlCfg.Storage.ConversationsDir
	}
	defaultSaveDir = GetEnv("CONVERSATIONS_DIR", defaultSaveDir)

	// Second pass: define and parse command-line flags
	fs := flag.NewFlagSet("proxy-server", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // prevent standard flag output during testing

	configFlag := fs.String("config", configPath, "Path to optional YAML configuration file")
	portFlag := fs.String("port", defaultPort, "Port to listen on (or env PORT)")
	targetFlag := fs.String("target", defaultTarget, "Target destination base URL (or env TARGET_URL)")
	logLevelFlag := fs.String("log-level", defaultLogLevel, "Log level: DEBUG, INFO, WARN, ERROR (or env LOG_LEVEL)")
	windowDurationFlag := fs.Duration("window-duration", defaultWindowDuration, "Sliding window duration (e.g. 60s, 5m)")
	maxRequestsFlag := fs.Int("window-max-requests", defaultMaxRequests, "Max requests allowed in sliding window (0 for unlimited)")
	maxTokensFlag := fs.Int("window-max-tokens", defaultMaxTokens, "Max estimated tokens in sliding window (0 for unlimited)")
	enforceLimitsFlag := fs.Bool("enforce-limits", defaultEnforceLimits, "Enforce sliding window limits (rejects with 429 when breached)")
	cbEnabledFlag := fs.Bool("cb-enabled", defaultCBEnabled, "Enable tool-call circuit breaker protection")
	cbClassAFlag := fs.Int("cb-class-a-limit", defaultCBClassA, "Max identical/similar tool calls allowed before tripping Class A circuit breaker")
	cbClassBFlag := fs.Int("cb-class-b-limit", defaultCBClassB, "Max accumulated tool errors before tripping Class B circuit breaker")
	cbHammingFlag := fs.Int("cb-max-hamming-dist", defaultCBHamming, "Max SimHash Hamming distance to treat tool calls as identical/near-duplicate (0-64 bits)")
	cbJaccardFlag := fs.Float64("cb-jaccard-threshold", defaultCBJaccard, "Jaccard similarity threshold for detecting near-duplicate tool calls (0.0 to 1.0)")
	velocityEnabledFlag := fs.Bool("velocity-enabled", defaultVelocityEnabled, "Enable session velocity detection")
	velocityMaxRPSFlag := fs.Float64("velocity-max-rps", defaultVelocityMaxRPS, "Max allowed requests per second per session (0 for unlimited)")
	velocityMaxRepeatsFlag := fs.Int("velocity-max-endpoint-repeats", defaultVelocityMaxRepeats, "Max hits to the same endpoint within repeat window (0 for unlimited)")
	velocityRepeatWindowFlag := fs.Duration("velocity-repeat-window", defaultVelocityRepeatWindow, "Time window for endpoint repeat tracking")
	saveConversationsFlag := fs.Bool("save-conversations", defaultSaveConversations, "Save conversations and tool calls as structured JSON")
	saveDirFlag := fs.String("save-dir", defaultSaveDir, "Directory to save structured conversation JSON records (or env CONVERSATIONS_DIR)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	// Parse Log Level
	var level slog.Level
	switch strings.ToUpper(*logLevelFlag) {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	// Parse and validate Target URL
	targetURL, err := url.Parse(*targetFlag)
	if err != nil {
		return nil, fmt.Errorf("invalid target URL %q: %w", *targetFlag, err)
	}

	appCfg := &AppConfig{
		Port:                       *portFlag,
		TargetURL:                  targetURL,
		TargetURLString:            *targetFlag,
		LogLevel:                   level,
		LogLevelString:             *logLevelFlag,
		WindowDuration:             *windowDurationFlag,
		MaxRequests:                *maxRequestsFlag,
		MaxTokens:                  *maxTokensFlag,
		EnforceLimits:              *enforceLimitsFlag,
		CBEnabled:                  *cbEnabledFlag,
		CBClassALimit:              *cbClassAFlag,
		CBClassBLimit:              *cbClassBFlag,
		CBMaxHammingDist:           *cbHammingFlag,
		CBJaccardThreshold:         *cbJaccardFlag,
		VelocityEnabled:            *velocityEnabledFlag,
		VelocityMaxRPS:             *velocityMaxRPSFlag,
		VelocityMaxEndpointRepeats: *velocityMaxRepeatsFlag,
		VelocityRepeatWindow:       *velocityRepeatWindowFlag,
		SaveConversations:          *saveConversationsFlag,
		SaveDir:                    *saveDirFlag,
		LoadedConfigFile:           loadedConfigFile,
	}

	if *configFlag != "" && *configFlag != loadedConfigFile {
		appCfg.LoadedConfigFile = *configFlag
	}

	if err := appCfg.Validate(); err != nil {
		return nil, err
	}

	return appCfg, nil
}
