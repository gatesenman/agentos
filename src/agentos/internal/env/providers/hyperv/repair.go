package hyperv

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
)

// RepairGroupResult is the outcome of attempting to add the user to Hyper-V Administrators.
type RepairGroupResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"` // zh
	LogPath string `json:"log_path,omitempty"`
	NeedRelogin bool `json:"need_relogin,omitempty"`
	Elevated bool  `json:"elevated,omitempty"`
}

// RepairHyperVGroup elevates (when possible) and adds the current interactive
// user + local Administrator to Hyper-V Administrators (S-1-5-32-578).
func RepairHyperVGroup(ctx context.Context) RepairGroupResult {
	if runtime.GOOS != "windows" {
		return RepairGroupResult{OK: false, Message: "仅 Windows 可修复 Hyper-V 组权限"}
	}
	return repairHyperVGroupPlatform(ctx)
}

// FindRepairScript returns path to hyperv-repair-group.ps1 next to the exe or in helpers.
func FindRepairScript() string {
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "hyperv-repair-group.ps1"),
			filepath.Join(dir, "bin", "hyperv-repair-group.ps1"),
			filepath.Join(dir, "..", "bin", "hyperv-repair-group.ps1"),
		)
	}
	if d, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(d, "packaging", "windows", "helpers", "hyperv-repair-group.ps1"))
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}
