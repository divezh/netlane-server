package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/decoy"
	"github.com/divezh/netlane-server/internal/endpoint"
	"github.com/divezh/netlane-server/internal/hoststats"
	"github.com/divezh/netlane-server/internal/node"
	"github.com/divezh/netlane-server/internal/store"
)

// prepareMain gives an existing main the secret sync prefix and saves it.
func prepareMain(cfg *config.Config) error {
	if !cfg.EnsureSyncPrefix() || cfg.Path == "" {
		return nil
	}
	return config.Save(cfg.Path, *cfg)
}

// joinNode registers this server with main and writes the node configuration.
func joinNode(path, joinURL, domain, name, country, dataDir, endpointVersion string) error {
	if _, err := os.Stat(path); err == nil {
		return errors.New("нода уже настроена; существующая конфигурация сохранена")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if joinURL == "" || domain == "" {
		return errors.New("укажите --url (ссылка из панели) и --domain (домен этой ноды)")
	}
	if err := config.ValidateServerIdentity(name, country); err != nil {
		return errors.New("название — до 64 символов; страна — две латинские буквы или пусто")
	}
	mainURL, _, _ := strings.Cut(joinURL, "/node/join")
	cfg, err := config.NewNode(domain, mainURL, dataDir)
	if err != nil {
		return err
	}
	secureUmask()
	pending, pendingErr := config.Load(path + ".pending")
	if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
		return pendingErr
	}
	if pendingErr == nil && (pending.Domain != cfg.Domain || pending.MainURL != cfg.MainURL || pending.NodeTokenFile != cfg.NodeTokenFile) {
		return errors.New("сохранённый join принадлежит другому домену/главному серверу; токен не изменён")
	}
	// A crash after saving the join token must not consume another invitation or
	// overwrite that token. The installer checks the persisted main/domain identity.
	if data, readErr := os.ReadFile(cfg.NodeTokenFile); readErr == nil {
		if pendingErr != nil {
			return errors.New("токен без исходной pending-конфигурации: восстановите конфигурацию из резервной копии; токен не изменён")
		}
		info, err := os.Lstat(cfg.NodeTokenFile)
		token := strings.TrimSpace(string(data))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || len(token) < 40 || len(token) > 128 || strings.IndexFunc(token, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
		}) >= 0 {
			return errors.New("сохранённый токен ноды повреждён или имеет небезопасные права; файл не изменён")
		}
		return config.SaveNew(path, cfg)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if pendingErr != nil {
		if err = config.SaveNew(path+".pending", cfg); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, token, err := node.Join(ctx, &http.Client{Timeout: 30 * time.Second}, joinURL, store.NodeJoin{Name: name, Country: country, Domain: domain, EndpointVersion: endpointVersion})
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(cfg.NodeTokenFile), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(cfg.NodeTokenFile), ".node-token-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err = file.Chmod(0600); err != nil {
		return err
	}
	if _, err = file.WriteString(token + "\n"); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Link(file.Name(), cfg.NodeTokenFile); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		dir, err := os.Open(filepath.Dir(cfg.NodeTokenFile))
		if err != nil {
			return err
		}
		err = dir.Sync()
		dir.Close()
		if err != nil {
			return err
		}
	}
	if err = config.SaveNew(path, cfg); err != nil {
		return err
	}
	host := mainURL
	if u, err := url.Parse(mainURL); err == nil {
		host = u.Host
	}
	fmt.Printf("Нода подключена к %s. Конфигурация: %s\n", host, path)
	return nil
}

func runNode(cfg config.Config) error {
	data, err := os.ReadFile(cfg.NodeTokenFile)
	if err != nil {
		return fmt.Errorf("токен ноды: %w", err)
	}
	control, err := endpoint.NewControl(cfg.ControlSocket)
	if err != nil {
		return err
	}
	metrics, err := endpoint.NewMetrics(cfg.MetricsAddress)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	site := &http.Server{Addr: cfg.DecoyListen, Handler: decoy.Handler(filepath.Join(filepath.Dir(cfg.NodeStateFile), "site")), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	go func() {
		if err := site.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			done <- fmt.Errorf("decoy site: %w", err)
		}
	}()
	defer site.Close()
	agent := &node.Agent{MainURL: cfg.MainURL, Token: strings.TrimSpace(string(data)), StatePath: cfg.NodeStateFile, Control: control, Clients: metrics.Clients}
	host := &hoststats.Sampler{}
	agent.Resources = func() store.Resources { return host.Read(filepath.Dir(cfg.NodeStateFile)) }
	go func() { done <- agent.Run(ctx) }()
	fmt.Printf("Netlane node %s: main %s\n", version, cfg.MainURL)
	select {
	case err = <-done:
		return err
	case <-ctx.Done():
		return nil
	}
}

func doctorNode(cfg config.Config) error {
	fmt.Printf("Нода. Домен: %s\nГлавный сервер: %s\n", cfg.Domain, cfg.MainURL)
	if _, err := os.Stat(cfg.NodeTokenFile); err != nil {
		return fmt.Errorf("нет токена ноды: %w", err)
	}
	synced, revision := node.LastSync(cfg.NodeStateFile)
	if synced.IsZero() {
		return errors.New("нода ещё ни разу не синхронизировалась с главным сервером")
	}
	fmt.Printf("Последняя синхронизация: %s, ревизия %d.\n", synced.Local().Format("2006-01-02 15:04:05"), revision)
	return nil
}
