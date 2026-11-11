package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

// check carries a single health item: machine-readable status plus a short
// explanation shown to the owner. The panel only reads; it never changes the server.
type check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | error | muted
	Detail string `json:"detail"`
}

type diagnosticsResult struct {
	Domain      string  `json:"domain"`
	Verdict     string  `json:"verdict"` // ok | warn | error
	GeneratedAt string  `json:"generated_at"`
	Checks      []check `json:"checks"`
}

// diagnostics probes everything that needs no extra privileges: DNS, the VPN
// ports, the managed endpoint and (when configured) the panel certificate.
func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	host := s.cfg.Domain
	out := diagnosticsResult{Domain: host, GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// 1. DNS resolution of the panel/vpn domain.
	if ips, err := resolv(host, ctx); err != nil {
		out.Checks = append(out.Checks, check{"DNS домена", "error", "Не удалось разрешить " + host + " (" + err.Error() + ")."})
	} else if len(ips) == 0 {
		out.Checks = append(out.Checks, check{"DNS домена", "error", "По домену " + host + " нет записей A."})
	} else {
		out.Checks = append(out.Checks, check{"DNS домена", "ok", "Домен " + host + " отвечает: " + ips[0] + "."})
	}

	// 2. TCP 443, then a real TLS handshake against the VPN domain.
	dialer := &net.Dialer{Timeout: 6 * time.Second}
	tcpConn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, "443"))
	if err != nil {
		out.Checks = append(out.Checks, check{"Порт 443 (TCP)", "error", "Не удаётся установить TCP-соединение с " + host + ":443."})
		out.Checks = append(out.Checks, check{"TLS 443", "muted", "Не проверялось: порт 443 недоступен."})
	} else {
		out.Checks = append(out.Checks, check{"Порт 443 (TCP)", "ok", "Порт 443 отвечает по TCP."})
		tlsConn := tls.Client(tcpConn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		hsErr := tlsConn.HandshakeContext(ctx)
		if hsErr == nil {
			out.Checks = append(out.Checks, check{"TLS 443", "ok", "TLS-рукопожатие успешно, сертификат проверен."})
		} else {
			out.Checks = append(out.Checks, check{"TLS 443", "warn", "Порт 443 открыт, но проверка сертификата не прошла (" + hsErr.Error() + ")."})
		}
		tlsConn.Close()
	}

	// 3. UDP 443 / QUIC. The panel cannot reliably probe UDP reachability; informational only.
	out.Checks = append(out.Checks, check{"Порт 443 (UDP/QUIC)", "muted", "Панель не проверяет UDP. Если HTTP/3 не подключается — проверьте UDP/443 у провайдера и в firewall."})

	// 4. Managed endpoint over the local control socket.
	if s.cfg.ControlSocket != "" {
		epCtx, epCancel := context.WithTimeout(ctx, 5*time.Second)
		state, epErr := s.control.Status(epCtx)
		epCancel()
		if epErr != nil {
			out.Checks = append(out.Checks, check{"Управляемый endpoint", "error", "Не удалось получить состояние: " + epErr.Error() + ". Доступ сохранён, но не подтверждается."})
		} else {
			out.Checks = append(out.Checks, check{"Управляемый endpoint", "ok", "На связи, пользователей: " + strconv.Itoa(len(state.Users)) + "."})
		}
	}

	// 5. Country database for client flags. Its absence is not a fault of the server,
	// but it is the reason no flags are drawn at all, so it must be visible.
	if s.geo.Available() {
		out.Checks = append(out.Checks, check{"База стран", "ok", "Загружена: панель рисует флаги клиентов."})
	} else {
		out.Checks = append(out.Checks, check{"База стран", "warn", "Не найдена: флаги клиентов не рисуются. Установите базу командой geoip-update или перезапустите панель."})
	}

	// 6. Panel TLS certificate (when the panel itself uses TLS on :8443).
	if s.cfg.TLSCert != "" {
		days, err := certificateDaysLeft(s.cfg.TLSCert)
		switch {
		case err != nil:
			out.Checks = append(out.Checks, check{"Сертификат панели", "warn", "Не удалось прочитать сертификат: " + err.Error() + "."})
		case days < 0:
			out.Checks = append(out.Checks, check{"Сертификат панели", "error", "Сертификат панели истёк."})
		case days <= 14:
			out.Checks = append(out.Checks, check{"Сертификат панели", "warn", "Сертификат панели истекает через " + strconv.Itoa(days) + " дн."})
		default:
			out.Checks = append(out.Checks, check{"Сертификат панели", "ok", "Сертификат панели действителен ещё " + strconv.Itoa(days) + " дн."})
		}
	}

	out.Verdict = "ok"
	for _, c := range out.Checks {
		if c.Status == "error" {
			out.Verdict = "error"
			break
		}
		if c.Status == "warn" {
			out.Verdict = "warn"
		}
	}
	respond(w, 200, out)
}

func resolv(host string, ctx context.Context) ([]string, error) {
	dnsCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupHost(dnsCtx, host)
}

// certificateDaysLeft returns full days until the leaf certificate expires, negative when expired.
func certificateDaysLeft(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return 0, errors.New("нет сертификата в формате PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return 0, err
	}
	return int(time.Until(cert.NotAfter).Hours() / 24), nil
}

// serviceInfo mirrors read-only panel facts and the SSH commands a root user would
// run on the server. None of them is executed by the panel.
type serviceInfo struct {
	Domain        string            `json:"domain"`
	PanelListen   string            `json:"panel_listen"`
	MetricsListen string            `json:"metrics_listen"`
	ControlSocket string            `json:"control_socket"`
	ServerName    string            `json:"server_name"`
	ServerCountry string            `json:"server_country"`
	Commands      map[string]string `json:"commands"`
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	host := s.cfg.Domain
	identity := s.identity()
	commands := maintenanceCommands()
	respond(w, 200, serviceInfo{
		Domain:        host,
		PanelListen:   s.cfg.Listen,
		MetricsListen: s.cfg.MetricsAddress,
		ControlSocket: s.cfg.ControlSocket,
		ServerName:    identity.ServerName,
		ServerCountry: identity.ServerCountry,
		Commands:      commands,
	})
}

// maintenanceCommands are shown to the owner as text; the panel never runs them.
// They follow the layout of deploy/dev/install.sh.
func maintenanceCommands() map[string]string {
	return map[string]string{
		"rename_domain": "Автоматической смены домена пока нет. Домен записан в настройках VPN, панели и сертификате, а у пользователей — в профилях.\nДля нового домена используйте отдельный чистый VPS и setup.sh, затем перенесите доступы и выдайте новые подписки. Не запускайте установщик поверх работающего сервера.",
		"renew_tls":     "Сертификат продлевается автоматически (certbot.timer); хук обновляет TLS, перечитывает сертификат VPN и перезапускает панель или агент ноды.\nПроверка по SSH от root:\nsystemctl status certbot.timer\ncertbot renew --dry-run",
		"backup":        "Резервная копия создаётся каждую ночь около 04:00 MSK (случайная задержка до 10 минут). Сделать её сейчас по SSH от root:\nsystemctl start trusttunnel-backup.service\nАрхивы лежат в /var/backups/trusttunnel (хранятся 14). В архиве ключи шифрования и TLS: копируйте его с VPS только в защищённое хранилище.",
		"update": `Загрузите архив релиза и .sha256 в /root. Выполните на главном сервере и каждой ноде:
cd /root
sha256sum -c trusttunnel-dev.tar.gz.sha256 &&
netlane_update_dir=$(mktemp -d /root/netlane-update.XXXXXX) &&
tar -xzf trusttunnel-dev.tar.gz -C "$netlane_update_dir" &&
bash "$netlane_update_dir/update.sh" --file /root/trusttunnel-dev.tar.gz
Обновлятор создаёт резервную копию; подключения могут кратковременно прерваться. Не используйте setup.sh для обновления.
Проверка установленной версии: /opt/trusttunnel-admin/bin/trusttunnel-admin version`,
	}
}
