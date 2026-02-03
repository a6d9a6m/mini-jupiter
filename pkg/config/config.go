package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

type OnChangeFunc func(cfg any)

type Option func(*Manager)

type Manager struct {
	v           *viper.Viper
	cfgType     reflect.Type
	current     atomic.Value
	mu          sync.Mutex
	onChange    []OnChangeFunc
	envPrefix   string
	enableWatch bool
}

func WithEnvPrefix(prefix string) Option {
	return func(m *Manager) {
		m.envPrefix = prefix
	}
}

func WithOnChange(fn OnChangeFunc) Option {
	return func(m *Manager) {
		if fn != nil {
			m.onChange = append(m.onChange, fn)
		}
	}
}

func WithWatch() Option {
	return func(m *Manager) {
		m.enableWatch = true
	}
}

func Load(path string, cfg any, opts ...Option) (*Manager, error) {
	cfgType, err := validateConfig(cfg)
	if err != nil {
		return nil, err
	}

	m := &Manager{
		v:       viper.New(),
		cfgType: cfgType,
	}
	for _, exe := range opts {
		exe(m)
	}

	if err := m.initViper(path); err != nil {
		return nil, err
	}
	if err := m.reloadInto(cfg); err != nil {
		return nil, err
	}
	m.current.Store(cfg)

	if m.enableWatch {
		m.watch()
	}
	return m, nil
}

func (m *Manager) Current() any {
	return m.current.Load()
}

func (m *Manager) watch() {
	m.v.WatchConfig()
	m.v.OnConfigChange(func(_ fsnotify.Event) {
		cfg := reflect.New(m.cfgType).Interface()
		if err := m.reloadInto(cfg); err != nil {
			return
		}
		m.current.Store(cfg)
		for _, fn := range m.onChange {
			fn(cfg)
		}
	})
}

func (m *Manager) reloadInto(cfg any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.v.ReadInConfig(); err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	if err := m.v.Unmarshal(cfg); err != nil {
		return fmt.Errorf("unmarshal config: %w", err)
	}
	return nil
}

func (m *Manager) initViper(path string) error {
	if path == "" {
		return errors.New("config path is empty")
	}
	m.v.SetConfigFile(path)

	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	if ext != "" {
		m.v.SetConfigType(ext)
	}

	if m.envPrefix != "" {
		m.v.SetEnvPrefix(m.envPrefix)
	}
	m.v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	m.v.AutomaticEnv()

	if err := bindEnvs(m.v, m.cfgType, ""); err != nil {
		return err
	}
	return nil
}

// 配置错误检查
func validateConfig(cfg any) (reflect.Type, error) {
	if cfg == nil {
		return nil, errors.New("cfg is nil")
	}
	cfgType := reflect.TypeOf(cfg)
	if cfgType.Kind() != reflect.Pointer {
		return nil, errors.New("cfg must be a pointer to struct")
	}
	cfgType = cfgType.Elem()
	if cfgType.Kind() != reflect.Struct {
		return nil, errors.New("cfg must be a pointer to struct")
	}
	return cfgType, nil
}

func bindEnvs(v *viper.Viper, t reflect.Type, prefix string) error {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		key, skip := fieldKey(field)
		if skip {
			continue
		}
		if prefix != "" {
			key = prefix + "." + key
		}

		ft := field.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct {
			if err := bindEnvs(v, ft, key); err != nil {
				return err
			}
			continue
		}
		if err := v.BindEnv(key); err != nil {
			return fmt.Errorf("bind env %s: %w", key, err)
		}
	}
	return nil
}

func fieldKey(field reflect.StructField) (string, bool) {
	if tag := field.Tag.Get("mapstructure"); tag != "" {
		if tag == "-" {
			return "", true
		}
		return tag, false
	}
	if tag := field.Tag.Get("yaml"); tag != "" {
		if tag == "-" {
			return "", true
		}
		return tag, false
	}
	if tag := field.Tag.Get("json"); tag != "" {
		if tag == "-" {
			return "", true
		}
		return tag, false
	}
	return strings.ToLower(field.Name), false
}
