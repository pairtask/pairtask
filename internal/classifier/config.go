package classifier

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/config"
)

// Modes. Off is the default; shadow records predictions and applies none.
const (
	ModeOff    = "off"
	ModeShadow = "shadow"
)

// Setting names, under the PAIRTASK_ prefix.
const (
	SettingMode      = "CLASSIFIER"
	SettingProvider  = "CLASSIFIER_PROVIDER"
	SettingAccount   = "CLASSIFIER_ACCOUNT"
	SettingModel     = "CLASSIFIER_MODEL"
	SettingThreshold = "CLASSIFIER_THRESHOLD"
	SettingKeyFile   = "CLASSIFIER_KEY_FILE"
	SettingRoles     = "CLASSIFIER_ROLES"
	SettingTimeout   = "CLASSIFIER_TIMEOUT"
)

// Defaults.
const (
	DefaultProvider      = ProviderWorkersAI
	DefaultModel         = "clef" // on Workers AI
	DefaultTypeSafeModel = "jev-latest"
	DefaultThreshold     = 0.7 // uncalibrated: shadow mode measures it, nothing acts on it
	DefaultTimeout       = 5 * time.Second
)

// Config is the classifier as configured. Key is the credential, held in
// memory only; it is never written, logged or passed on a command line.
type Config struct {
	Mode      string
	Provider  string
	Account   string
	Model     string
	Threshold float64
	Timeout   time.Duration
	Roles     map[string]string // agent ID -> one-line role, optional
	Key       string
}

// Enabled: predictions are made.
func (c Config) Enabled() bool { return c.Mode == ModeShadow }

// Files reads the key and roles files; tests supply fakes. Owner reports
// a file's owner uid, when the platform knows it; nil means unknown.
type Files struct {
	ReadFile func(name string) ([]byte, error)
	Stat     func(name string) (fs.FileInfo, error)
	Owner    func(info fs.FileInfo) (uid int, known bool)
	UID      int // the broker's uid, which must own the key file
}

// Load reads the settings. An invalid explicit value is an error. A shadow
// mode without a credential is not: it comes back disabled, and status
// says why, so broadcast and claim behave as before.
func Load(lookup config.LookupFunc, files Files) (cfg Config, status string, err error) {
	cfg = Config{Mode: ModeOff, Provider: DefaultProvider, Model: DefaultModel, Threshold: DefaultThreshold, Timeout: DefaultTimeout}
	shadow, err := shadowAsked(lookup)
	if err != nil || !shadow {
		return cfg, "classifier off", err
	}
	status, err = loadKey(lookup, files.real(), &cfg)
	if err != nil || status != "" {
		return cfg, status, err
	}
	if err := loadSettings(lookup, files, &cfg); err != nil {
		return cfg, "", err
	}
	cfg.Mode = ModeShadow
	return cfg, "classifier in shadow mode: " + cfg.Provider + " " + cfg.Model, nil
}

// shadowAsked reads the mode: off (or unset) is false, shadow is true,
// anything else is an error.
func shadowAsked(lookup config.LookupFunc) (bool, error) {
	mode, given, err := config.Explicit(lookup, SettingMode)
	off := !given || mode == ModeOff
	if err != nil || off {
		return false, err
	}
	if mode != ModeShadow {
		return false, fmt.Errorf("%s must be %s or %s, not %q", config.Name(SettingMode), ModeOff, ModeShadow, mode)
	}
	return true, nil
}

// real fills the owner hook from the OS when a test did not fake it.
func (f Files) real() Files {
	if f.Owner == nil {
		f.Owner, f.UID = FileOwner, os.Getuid()
	}
	return f
}

// loadSettings reads every setting after the credential.
func loadSettings(lookup config.LookupFunc, files Files, cfg *Config) error {
	for _, load := range []func(config.LookupFunc, *Config) error{loadProvider, loadModel, loadBounds} {
		if err := load(lookup, cfg); err != nil {
			return err
		}
	}
	return loadRoles(lookup, files, cfg)
}

// setting applies parse to an explicitly given setting; absent is fine.
func setting(lookup config.LookupFunc, name string, parse func(string) error) error {
	raw, given, err := config.Explicit(lookup, name)
	if err != nil || !given {
		return err
	}
	return parse(raw)
}

// loadProvider reads the provider and, for Workers AI, the account.
func loadProvider(lookup config.LookupFunc, cfg *Config) error {
	provider, given, err := config.Explicit(lookup, SettingProvider)
	if err != nil {
		return err
	}
	if given {
		if _, ok := origins[provider]; !ok {
			return fmt.Errorf("%s must be %s or %s, not %q", config.Name(SettingProvider), ProviderWorkersAI, ProviderTypeSafe, provider)
		}
		cfg.Provider = provider
	}
	cfg.Account, _, _ = config.LookupIn(lookup, SettingAccount)
	if cfg.Provider == ProviderWorkersAI && cfg.Account == "" {
		return errors.New(config.Name(SettingAccount) + " is required for " + ProviderWorkersAI)
	}
	return nil
}

// loadModel reads the model, defaulting by provider.
func loadModel(lookup config.LookupFunc, cfg *Config) error {
	if cfg.Provider == ProviderTypeSafe {
		cfg.Model = DefaultTypeSafeModel
	}
	model, given, err := config.Explicit(lookup, SettingModel)
	if given {
		cfg.Model = model
	}
	return err
}

// loadBounds reads the confidence threshold and the provider timeout.
func loadBounds(lookup config.LookupFunc, cfg *Config) error {
	if err := setting(lookup, SettingThreshold, func(raw string) (err error) { cfg.Threshold, err = threshold(raw); return err }); err != nil {
		return err
	}
	return setting(lookup, SettingTimeout, func(raw string) (err error) { cfg.Timeout, err = timeout(raw); return err })
}

// threshold is a finite number in [0, 1].
func threshold(raw string) (float64, error) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || !unit(value) {
		return 0, errors.New(config.Name(SettingThreshold) + " must be a number between 0 and 1")
	}
	return value, nil
}

// timeout is a duration up to a minute.
func timeout(raw string) (time.Duration, error) {
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 || value > time.Minute {
		return 0, errors.New(config.Name(SettingTimeout) + " must be a duration up to 1m")
	}
	return value, nil
}

// loadRoles reads the optional roles file: a JSON object, agent ID → role line.
func loadRoles(lookup config.LookupFunc, files Files, cfg *Config) error {
	path, given, err := config.Explicit(lookup, SettingRoles)
	if err != nil || !given {
		return err
	}
	data, err := files.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s: %w", config.Name(SettingRoles), err)
	}
	roles := map[string]string{}
	if err := json.Unmarshal(data, &roles); err != nil {
		return errors.New(config.Name(SettingRoles) + " must be a JSON object of agent ID to role")
	}
	for id, role := range roles {
		roles[id] = strings.TrimSpace(strings.SplitN(role, "\n", 2)[0])
	}
	cfg.Roles = roles
	return nil
}

// keyFileUsable: a regular file, readable by its owner only, owned by the
// broker user. Anything else is a misconfiguration, not an absence.
func keyFileUsable(info fs.FileInfo, files Files) error {
	if !info.Mode().IsRegular() {
		return errors.New(config.Name(SettingKeyFile) + " must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must be readable by its owner only (mode %04o)", config.Name(SettingKeyFile), info.Mode().Perm())
	}
	if uid, known := files.Owner(info); known && uid != files.UID {
		return fmt.Errorf("%s must be owned by the broker user (uid %d), not uid %d", config.Name(SettingKeyFile), files.UID, uid)
	}
	return nil
}

// loadKey reads the credential file. Unset: disabled, with the reason as
// status. A file others can read is refused: that is a misconfiguration,
// not an absence.
func loadKey(lookup config.LookupFunc, files Files, cfg *Config) (status string, err error) {
	path, given, err := config.Explicit(lookup, SettingKeyFile)
	if err != nil {
		return "", err
	}
	if !given {
		return "classifier disabled: " + config.Name(SettingKeyFile) + " is not set", nil
	}
	info, err := files.Stat(path)
	if err != nil {
		return "classifier disabled: credential file unreadable", nil
	}
	if err := keyFileUsable(info, files); err != nil {
		return "", err
	}
	data, err := files.ReadFile(path)
	if err != nil {
		return "classifier disabled: credential file unreadable", nil
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "classifier disabled: credential file is empty", nil
	}
	cfg.Key = key
	return "", nil
}
