//go:build !windows

package hyperv

import "context"

func repairHyperVGroupPlatform(ctx context.Context) RepairGroupResult {
	return RepairGroupResult{OK: false, Message: "仅 Windows 可修复 Hyper-V 组权限"}
}
