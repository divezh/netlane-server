package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCertificateDaysLeftRejectsNonPEM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := certificateDaysLeft(path); err == nil {
		t.Fatal("a file without a PEM certificate must be reported as unreadable, not as expiring in 0 days")
	}
}

func TestMaintenanceCommandsMatchInstalledLayout(t *testing.T) {
	commands := maintenanceCommands()
	if !strings.Contains(commands["backup"], "systemctl start trusttunnel-backup.service") || strings.Contains(commands["backup"], "/root/trusttunnel-install") {
		t.Fatalf("backup command must use the installed unit: %q", commands["backup"])
	}
	// deploy/dev/install.sh refuses a VPS that already has a configuration, so it is not an update path.
	if !strings.Contains(commands["update"], "/opt/trusttunnel-admin/bin/trusttunnel-admin") || strings.Contains(commands["update"], "scripts/deploy-vps.sh root@") {
		t.Fatalf("update must describe replacing the installed binary, not re-running the fresh-VPS installer: %q", commands["update"])
	}
}
