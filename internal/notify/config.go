package notify

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/pairtask/pairtask/internal/config"
)

// Modes. Off is the default.
const (
	ModeOff   = "off"
	ModeSlack = "slack"
)

// Setting names, under the PAIRTASK_ prefix.
const (
	SettingMode       = "NOTIFY"
	SettingSecretFile = "NOTIFY_SECRET_FILE"
	SettingHidden     = "NOTIFY_HIDDEN"
	SettingWorkspace  = "NOTIFY_WORKSPACE"
	SettingTimeout    = "NOTIFY_TIMEOUT"
)

// DefaultTimeout bounds one post.
const DefaultTimeout = 10 * time.Second

// Config is the notifier as configured. Secret is the webhook URL, held in
// memory only.
type Config struct {
	Mode      string
	Secret    string
	Hidden    bool
	Workspace string
	Timeout   time.Duration
}

// Enabled: notices are posted.
func (c Config) Enabled() bool { return c.Mode == ModeSlack }

// Files reads the secret file; tests supply fakes. Owner reports a file's
// owner uid when the platform knows it; nil means the OS's.
type Files struct {
	ReadFile func(name string) ([]byte, error)
	Stat     func(name string) (fs.FileInfo, error)
	Owner    func(info fs.FileInfo) (uid int, known bool)
	UID      int
}

func (f Files) real() Files {
	if f.Owner == nil {
		f.Owner, f.UID = FileOwner, os.Getuid()
	}
	return f
}

// Load reads the settings. An invalid explicit value is an error. Slack
// mode without a usable secret is not: it comes back disabled, and status
// says why.
func Load(lookup config.LookupFunc, files Files) (cfg Config, status string, err error) {
	cfg = Config{Mode: ModeOff, Timeout: DefaultTimeout}
	mode, given, err := config.Explicit(lookup, SettingMode)
	off := !given || mode == ModeOff
	if err != nil || off {
		return cfg, "notifications off", err
	}
	if mode != ModeSlack {
		return cfg, "", fmt.Errorf("%s must be %s or %s, not %q", config.Name(SettingMode), ModeOff, ModeSlack, mode)
	}
	status, err = loadSecret(lookup, files.real(), &cfg)
	if err != nil || status != "" {
		return cfg, status, err
	}
	if err := loadOptions(lookup, &cfg); err != nil {
		return cfg, "", err
	}
	cfg.Mode = ModeSlack
	return cfg, "notifications on: slack", nil
}

// loadOptions reads the hidden flag, the workspace label and the timeout.
func loadOptions(lookup config.LookupFunc, cfg *Config) error {
	hidden, _, _ := config.LookupIn(lookup, SettingHidden)
	cfg.Hidden = hidden == "1" || hidden == "true"
	cfg.Workspace, _, _ = config.LookupIn(lookup, SettingWorkspace)
	raw, given, err := config.Explicit(lookup, SettingTimeout)
	if err != nil || !given {
		return err
	}
	value, err := time.ParseDuration(raw)
	if err != nil || !validTimeout(value) {
		return errors.New(config.Name(SettingTimeout) + " must be a duration up to 1m")
	}
	cfg.Timeout = value
	return nil
}

func validTimeout(value time.Duration) bool { return value > 0 && value <= time.Minute }

// loadSecret reads the webhook URL from its file: a regular file readable
// by its owner only, owned by the broker user. Unset, unreadable or empty
// disables; a file others can read, or a URL that is not a Slack webhook,
// is an error.
func loadSecret(lookup config.LookupFunc, files Files, cfg *Config) (status string, err error) {
	path, given, err := config.Explicit(lookup, SettingSecretFile)
	if err != nil {
		return "", err
	}
	if !given {
		return "notifications disabled: " + config.Name(SettingSecretFile) + " is not set", nil
	}
	info, err := files.Stat(path)
	if err != nil {
		return "notifications disabled: secret file unreadable", nil
	}
	if err := secretFileUsable(info, files); err != nil {
		return "", err
	}
	data, err := files.ReadFile(path)
	if err != nil {
		return "notifications disabled: secret file unreadable", nil
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "notifications disabled: secret file is empty", nil
	}
	if err := CheckWebhookURL(secret); err != nil {
		return "", err
	}
	cfg.Secret = secret
	return "", nil
}

func secretFileUsable(info fs.FileInfo, files Files) error {
	if !info.Mode().IsRegular() {
		return errors.New(config.Name(SettingSecretFile) + " must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must be readable by its owner only (mode %04o)", config.Name(SettingSecretFile), info.Mode().Perm())
	}
	if uid, known := files.Owner(info); known && uid != files.UID {
		return fmt.Errorf("%s must be owned by the broker user (uid %d), not uid %d", config.Name(SettingSecretFile), files.UID, uid)
	}
	return nil
}
