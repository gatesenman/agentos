package main

import (
	"os"
	"path/filepath"
)

// writeInitialAdminPasswordFile keeps server/initial-admin-password.txt aligned
// with the current admin password so tray «初始密码» and login reveal stay valid
// after reset-password or an in-UI password change.
func writeInitialAdminPasswordFile(serverDir, pw string) error {
	if serverDir == "" || pw == "" {
		return nil
	}
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(serverDir, "initial-admin-password.txt"), []byte(pw+"\n"), 0o600)
}
