package fresh

import (
	"errors"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/witseko/agentcy/internal/paths"
	"github.com/witseko/agentcy/internal/wire"
)

// SettingsFilename is the name of the plugin settings file inside the data directory.
const SettingsFilename = "plugins.json"

// maxSettingsBytes is the largest plugins.json that is read.
const maxSettingsBytes = 65_536

// The two errors of a bad settings file, with Python's text. Neither repeats the file.
var (
	ErrSettingsUnsafe  = errors.New("Plugin configuration must be a private file")
	ErrSettingsInvalid = errors.New("Invalid fresh-run plugin configuration")
)

// The limits of one plugin entry, from plugin_settings.py and plugin_contract.py.
const (
	defaultTimeoutSeconds = 300
	maxTimeoutSeconds     = 3600
	maxCap                = 100_000_000
	maxCommandArguments   = 32
	maxArgumentLength     = 4096
	maxModelLength        = 256
	maxEnvironmentNames   = 32
)

var (
	pluginNamePattern      = regexp.MustCompile(`^[a-zA-Z0-9-]{1,64}$`)
	environmentNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)
)

// Plugin is one local executable with its user-approved model and limits.
type Plugin struct {
	Name            string
	Command         []string
	Model           string
	TimeoutSeconds  int64
	TokenCap        *int64
	CostCapMicroUSD *int64
	Environment     []string
}

// Settings is the parsed plugins.json: the registered plugins sorted by name.
type Settings struct {
	Plugins []Plugin
}

// clonePlugin returns an independent copy of plugin so callers cannot mutate cached
// settings.
func clonePlugin(plugin Plugin) *Plugin {
	copied := plugin
	copied.Command = slices.Clone(plugin.Command)
	copied.Environment = slices.Clone(plugin.Environment)
	if plugin.TokenCap != nil {
		v := *plugin.TokenCap
		copied.TokenCap = &v
	}
	if plugin.CostCapMicroUSD != nil {
		v := *plugin.CostCapMicroUSD
		copied.CostCapMicroUSD = &v
	}
	return &copied
}

// Find returns a copy of the plugin registered under name, or nil.
func (settings *Settings) Find(name string) *Plugin {
	for index := range settings.Plugins {
		if settings.Plugins[index].Name == name {
			return clonePlugin(settings.Plugins[index])
		}
	}
	return nil
}

// ErrPluginNotConfigured is the error of a plugin name that plugins.json lacks.
var ErrPluginNotConfigured = errors.New("Fresh-run plugin is not configured")

// Plugin loads the settings and returns the plugin registered under name
// (configured_plugin in plugin_settings.py).
func (file *SettingsFile) Plugin(name string) (*Plugin, error) {
	settings, err := file.Load()
	if err != nil {
		return nil, err
	}
	if settings == nil {
		return nil, ErrPluginNotConfigured
	}
	plugin := settings.Find(name)
	if plugin == nil {
		return nil, ErrPluginNotConfigured
	}
	return plugin, nil
}

// SettingsFile loads plugins.json and keeps the parsed result while the file's
// modification time, size and inode stay the same (port plan fix L5). The safety checks
// run on every Load. It is safe for concurrent use.
type SettingsFile struct {
	file *paths.PrivateConfigFile[*Settings]
}

// NewSettingsFile returns the loader of plugins.json inside dataDir.
func NewSettingsFile(dataDir string) *SettingsFile {
	return &SettingsFile{
		file: paths.NewPrivateConfigFile(
			filepath.Join(dataDir, SettingsFilename),
			maxSettingsBytes,
			ErrSettingsUnsafe,
			ErrSettingsInvalid,
			ParseSettings,
		),
	}
}

// Load returns the settings, or nil and no error when plugins.json does not exist.
func (file *SettingsFile) Load() (*Settings, error) {
	return file.file.Load()
}

// ParseSettings validates the content of plugins.json like the pydantic models: an
// object with only "plugins", whose keys are plugin names and whose values are plugin
// entries without unknown fields. Every problem returns ErrSettingsInvalid.
func ParseSettings(data []byte) (*Settings, error) {
	document, err := wire.Parse(data)
	if err != nil || !document.OnlyKnownFields("plugins") {
		return nil, ErrSettingsInvalid
	}
	settings := &Settings{Plugins: []Plugin{}}
	table, found := document.Get("plugins")
	if !found {
		return settings, nil
	}
	if table.Kind != wire.Object {
		return nil, ErrSettingsInvalid
	}
	for _, name := range table.Keys() {
		entry, _ := table.Get(name)
		plugin, valid := parsePlugin(entry)
		if !valid || !pluginNamePattern.MatchString(name) {
			return nil, ErrSettingsInvalid
		}
		plugin.Name = name
		settings.Plugins = append(settings.Plugins, plugin)
	}
	sort.Slice(settings.Plugins, func(left, right int) bool {
		return settings.Plugins[left].Name < settings.Plugins[right].Name
	})
	return settings, nil
}

// parsePlugin validates one plugin entry; command and model are required.
func parsePlugin(value wire.Value) (Plugin, bool) {
	plugin := Plugin{TimeoutSeconds: defaultTimeoutSeconds, Environment: []string{}}
	if !value.OnlyKnownFields("command", "model", "timeout_seconds", "token_cap", "cost_cap_microusd", "environment") {
		return plugin, false
	}
	command, hasCommand := value.Get("command")
	model, hasModel := value.Get("model")
	if !hasCommand || !hasModel {
		return plugin, false
	}
	valid := true
	plugin.Command, valid = parseCommand(command)
	if valid {
		plugin.Model, valid = boundedText(model, 1, maxModelLength)
	}
	if entry, found := value.Get("timeout_seconds"); found && valid {
		plugin.TimeoutSeconds, valid = laxInteger(entry, 1, maxTimeoutSeconds)
	}
	if entry, found := value.Get("token_cap"); found && valid {
		plugin.TokenCap, valid = optionalCap(entry)
	}
	if entry, found := value.Get("cost_cap_microusd"); found && valid {
		plugin.CostCapMicroUSD, valid = optionalCap(entry)
	}
	if entry, found := value.Get("environment"); found && valid {
		plugin.Environment, valid = parseEnvironment(entry)
	}
	return plugin, valid
}

// parseCommand validates the argument list: 1 to 32 arguments of at most 4096 code
// points without NUL, the first an absolute path.
func parseCommand(value wire.Value) ([]string, bool) {
	if value.Kind != wire.Array || len(value.Items) == 0 || len(value.Items) > maxCommandArguments {
		return nil, false
	}
	command := make([]string, 0, len(value.Items))
	for _, item := range value.Items {
		argument, valid := boundedText(item, 0, maxArgumentLength)
		if !valid || strings.ContainsRune(argument, 0) {
			return nil, false
		}
		command = append(command, argument)
	}
	return command, filepath.IsAbs(command[0])
}

// parseEnvironment validates the list of environment variable names to pass.
func parseEnvironment(value wire.Value) ([]string, bool) {
	if value.Kind != wire.Array || len(value.Items) > maxEnvironmentNames {
		return nil, false
	}
	names := make([]string, 0, len(value.Items))
	for _, item := range value.Items {
		if item.Kind != wire.String || !environmentNamePattern.MatchString(item.Text) {
			return nil, false
		}
		names = append(names, item.Text)
	}
	return names, true
}

// boundedText accepts a JSON string whose code point length is within bounds.
func boundedText(value wire.Value, minimum, maximum int) (string, bool) {
	if value.Kind != wire.String {
		return "", false
	}
	length := utf8.RuneCountInString(value.Text)
	return value.Text, length >= minimum && length <= maximum
}

// optionalCap accepts null as nil, or an integer from 1 to 100,000,000.
func optionalCap(value wire.Value) (*int64, bool) {
	if value.Kind == wire.Null {
		return nil, true
	}
	number, valid := laxInteger(value, 1, maxCap)
	if !valid {
		return nil, false
	}
	return &number, true
}

// laxInteger accepts an integer in bounds the way a non-strict pydantic int reads JSON:
// an integer, a float without a fraction, a string holding an integer, or a boolean.
func laxInteger(value wire.Value, minimum, maximum int64) (int64, bool) {
	var number int64
	switch value.Kind {
	case wire.Number:
		parsed, err := strconv.ParseFloat(value.Literal, 64)
		if err != nil || parsed != math.Trunc(parsed) || math.Abs(parsed) > 1e15 {
			return 0, false
		}
		number = int64(parsed)
	case wire.String:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value.Text), 10, 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	case wire.Bool:
		if value.Boolean {
			number = 1
		}
	default:
		return 0, false
	}
	return number, number >= minimum && number <= maximum
}
