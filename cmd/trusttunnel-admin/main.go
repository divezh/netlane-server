package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/divezh/netlane-server/internal/api"
	"github.com/divezh/netlane-server/internal/config"
	"github.com/divezh/netlane-server/internal/decoy"
	"github.com/divezh/netlane-server/internal/endpoint"
	"github.com/divezh/netlane-server/internal/geoip"
	"github.com/divezh/netlane-server/internal/store"
	"golang.org/x/term"
)

// version is the panel build; api.Version shows it and compares it with the release channel.
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("команды: setup, serve, join, node, doctor, passwd, reset-2fa, version")
	}
	if args[0] == "version" {
		fmt.Println(version)
		return nil
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	path := fs.String("config", "/etc/trusttunnel-admin/config.json", "путь к конфигурации")
	domain := fs.String("domain", "", "домен VPN")
	admin := fs.String("admin", "admin", "логин администратора")
	dataDir := fs.String("data-dir", "/var/lib/trusttunnel-admin", "каталог базы")
	controlSocket := fs.String("control-socket", "", "локальный сокет управляемого endpoint (для разработки)")
	passwordStdin := fs.Bool("password-stdin", false, "читать пароль из stdin для автоматизированной установки")
	joinURL := fs.String("url", "", "ссылка подключения ноды из панели (join)")
	nodeName := fs.String("name", "", "название ноды в профилях (join)")
	nodeCountry := fs.String("country", "", "страна ноды, две латинские буквы (join)")
	endpointVersion := fs.String("endpoint-version", "", "версия движка TrustTunnel (join)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "setup" {
		if _, err := os.Stat(*path); err == nil {
			return errors.New("панель уже настроена; существующая конфигурация сохранена")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if !*passwordStdin && !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("мастер требует терминал для безопасного ввода пароля")
		}
		reader := bufio.NewReader(os.Stdin)
		if *passwordStdin && *domain == "" {
			return errors.New("для --password-stdin укажите --domain")
		}
		if *domain == "" {
			fmt.Print("Домен VPN: ")
			line, err := reader.ReadString('\n')
			if err != nil {
				return err
			}
			*domain = strings.TrimSpace(line)
		}
		var password []byte
		if *passwordStdin {
			line, err := reader.ReadString('\n')
			if err != nil {
				return errors.New("не удалось прочитать строку пароля из stdin")
			}
			password = []byte(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
		} else {
			fmt.Printf("Пароль администратора %s (от 12 символов): ", *admin)
			var err error
			password, err = term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				return err
			}
			fmt.Print("Повторите пароль: ")
			confirm, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Println()
			if err != nil {
				return err
			}
			if string(password) != string(confirm) {
				return errors.New("пароли не совпали")
			}
		}
		cfg, err := config.New(*domain, *admin, string(password), *dataDir)
		if err != nil {
			return err
		}
		cfg.ControlSocket = *controlSocket
		if err = cfg.Validate(); err != nil {
			return err
		}
		// Persist the encryption key before creating the database. After an interruption
		// the installer can reopen that database using the same config/key.
		if _, err := os.Stat(cfg.Database); err == nil {
			return errors.New("база уже существует без конфигурации; восстановите исходный config.json из резервной копии")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		secureUmask()
		if err = config.SaveNew(*path, cfg); err != nil {
			return err
		}
		key, _ := cfg.Key()
		db, err := store.Open(cfg.Database, key)
		if err != nil {
			return err
		}
		if err = db.Close(); err != nil {
			return err
		}
		fmt.Printf("Панель настроена. Конфигурация: %s\n", *path)
		return nil
	}
	if args[0] == "join" {
		return joinNode(*path, *joinURL, *domain, *nodeName, *nodeCountry, *dataDir, *endpointVersion)
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if args[0] == "node" {
		if !cfg.IsNode() {
			return errors.New("эта конфигурация не нода; главный сервер запускается командой serve")
		}
		return runNode(cfg)
	}
	if cfg.IsNode() && (args[0] == "serve" || args[0] == "passwd" || args[0] == "reset-2fa") {
		return errors.New("это конфигурация ноды: запустите trusttunnel-admin node")
	}
	if args[0] == "passwd" {
		return changePassword(cfg, *passwordStdin)
	}
	if args[0] == "reset-2fa" {
		return resetFactor(cfg)
	}
	if args[0] == "doctor" && cfg.IsNode() {
		return doctorNode(cfg)
	}
	if args[0] == "doctor" {
		fmt.Printf("Конфигурация корректна. Домен: %s\nБаза: %s\nМетрики: %s\n", cfg.Domain, filepath.Clean(cfg.Database), cfg.MetricsAddress)
		if cfg.ControlSocket == "" {
			fmt.Println("Управление endpoint не настроено.")
		} else {
			client, err := endpoint.NewControl(cfg.ControlSocket)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			state, err := client.Status(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("Управляемый endpoint подключён: поколение %d, пользователей %d.\n", state.Generation, len(state.Users))
		}
		return nil
	}
	if args[0] != "serve" {
		return errors.New("неизвестная команда")
	}
	secureUmask()
	if err = prepareMain(&cfg); err != nil {
		return err
	}
	key, _ := cfg.Key()
	db, err := store.Open(cfg.Database, key)
	if err != nil {
		return err
	}
	defer db.Close()
	hub := api.NewHub()
	ensureGeoIP(cfg)
	// The panel reports its build to the release channel, so the update card knows what is installed.
	api.Version = version
	handler, err := api.New(cfg, db, hub)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Who is online and from where, sampled from the endpoint metrics; stops before the database closes.
	presenceCtx, stopPresence := context.WithCancel(ctx)
	presenceDone := make(chan struct{})
	go func() { api.TrackPresence(presenceCtx, cfg.MetricsAddress, db); close(presenceDone) }()
	defer func() { stopPresence(); <-presenceDone }()
	statisticsDone := make(chan struct{})
	go func() { api.TrackStatistics(presenceCtx, cfg.MetricsAddress, cfg.Database, db); close(statisticsDone) }()
	defer func() { stopPresence(); <-statisticsDone }()
	done := make(chan error, 1)
	go func() {
		if cfg.TLSCert != "" {
			done <- server.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			done <- server.ListenAndServe()
		}
	}()
	// The decoy website behind the VPN endpoint: an own site in <data dir>/site replaces the built-in one.
	var decoyServer *http.Server
	if cfg.DecoyListen != "" {
		// Nodes and subscriptions live under the secret prefix; a node's sync is a long poll.
		site := api.SyncHandler(cfg, db, hub, decoy.Handler(filepath.Join(filepath.Dir(cfg.Database), "site")), time.Now)
		decoyServer = &http.Server{Addr: cfg.DecoyListen, Handler: site, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
		go func() {
			if err := decoyServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				done <- fmt.Errorf("decoy site: %w", err)
			}
		}()
		defer decoyServer.Close()
	}
	fmt.Printf("Netlane Console %s: %s\n", version, cfg.Listen)
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}

// changePassword resets the administrator password from the server console, for when
// it is forgotten. The running panel reads the password at start, so it is restarted after.
func changePassword(cfg config.Config, fromStdin bool) error {
	var password []byte
	if fromStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return errors.New("не удалось прочитать строку пароля из stdin")
		}
		password = []byte(strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
	} else {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("для смены пароля нужен терминал или --password-stdin")
		}
		fmt.Printf("Новый пароль администратора %s (от 12 символов): ", cfg.Admin)
		first, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return err
		}
		fmt.Print("Повторите пароль: ")
		confirm, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return err
		}
		if string(first) != string(confirm) {
			return errors.New("пароли не совпали")
		}
		password = first
	}
	if err := cfg.SetPassword(string(password)); err != nil {
		return errors.New("пароль должен содержать от 12 до 1024 байт")
	}
	if err := config.Save(cfg.Path, cfg); err != nil {
		return err
	}
	fmt.Println("Пароль администратора изменён. Примените его: systemctl restart trusttunnel-admin")
	fmt.Println("После перезапуска все открытые сессии панели завершатся.")
	fmt.Println("Если потерян и телефон с кодами 2FA, и коды восстановления: trusttunnel-admin reset-2fa")
	return nil
}

// resetFactor turns the panel's second factor off; only root on the server can run it.
// The panel reads the factor at every login, so no restart is needed.
func resetFactor(cfg config.Config) error {
	key, err := cfg.Key()
	if err != nil {
		return err
	}
	db, err := store.Open(cfg.Database, key)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	sealed, err := db.AdminFactor(ctx)
	if err != nil {
		return err
	}
	if sealed == "" {
		fmt.Println("Двухфакторная проверка уже выключена.")
		return nil
	}
	if ok, err := db.ReplaceAdminFactor(ctx, sealed, ""); err != nil || !ok {
		return errors.New("не удалось выключить 2FA: настройка изменилась одновременно, повторите команду")
	}
	if err = db.Audit(ctx, "root", "admin.2fa_reset", "cli"); err != nil {
		return err
	}
	fmt.Println("Двухфакторная проверка выключена. Войдите в панель по паролю и включите 2FA заново.")
	return nil
}

// ensureGeoIP installs the country database when the file is missing. A server that was
// updated rather than installed can arrive without the systemd timer that fetches it, and
// without the database the panel draws no flags for anyone. The download is best effort
// and bounded: a failure costs flags, never the panel.
func ensureGeoIP(cfg config.Config) {
	path := cfg.GeoIPFile()
	if _, err := os.Stat(path); err == nil {
		return
	}
	// Bounded so a slow or blocked download cannot hold the panel back: the updater
	// waits a minute for /healthz after a restart, and this must stay well inside it.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := geoip.Ensure(ctx, path, time.Now()); err != nil {
		log.Printf("geoip: база стран не установлена, флаги рисоваться не будут: %v", err)
		return
	}
	log.Printf("geoip: база стран установлена: %s", path)
}
