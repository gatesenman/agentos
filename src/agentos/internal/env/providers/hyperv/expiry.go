package hyperv

import (
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// parseSystemTime decodes the "Kernel-ExpirationDate" license value of
// preview builds: little-endian uint16 year, month, day, hour, minute, second
// (observed on build 27902: EA07 0800 0B00 1200 0900 2C00 = 2026-08-11 18:09:44).
func parseSystemTime(b []byte) (time.Time, bool) {
	if len(b) < 12 {
		return time.Time{}, false
	}
	u := func(i int) int { return int(binary.LittleEndian.Uint16(b[i*2:])) }
	y, mo, d := u(0), u(1), u(2)
	if y < 2000 || mo < 1 || mo > 12 || d < 1 || d > 31 {
		return time.Time{}, false
	}
	return time.Date(y, time.Month(mo), d, u(3), u(4), u(5), 0, time.UTC), true
}

// expiredBuildReason explains why no VM can start on an expired Windows
// Insider build (the Hyper-V firmware then fails with 0x80090006 "invalid
// signature"). Empty when the build has no expiry or has not expired.
func expiredBuildReason(exp time.Time, build string, now time.Time) string {
	if exp.IsZero() || now.Before(exp) {
		return ""
	}
	return fmt.Sprintf("此 Windows 预览版（Insider 内部版本 %s）已于 %s 过期：Hyper-V 无法加载虚拟机固件（错误 0x80090006「无效签名」），任何虚拟机都无法启动。请在「设置 → Windows 更新」中升级到最新预览版或改装正式版 Windows 11，重启后再创建本地虚拟机。",
		build, exp.In(time.Local).Format("2006-01-02"))
}

// friendlyStartError maps well-known Start-VM failures to actionable text.
func friendlyStartError(msg string) string {
	switch {
	case strings.Contains(msg, "0x80090006"):
		return "Hyper-V 拒绝启动虚拟机：虚拟机固件签名无效（0x80090006）。这通常是 Windows 预览版（Insider）已过期或系统更新未完成导致的，请运行 Windows 更新并重启电脑后重试。原始错误：" + msg
	case strings.Contains(msg, "0x8007000D") || strings.Contains(strings.ToLower(msg), "hypervisor is not running") || strings.Contains(msg, "虚拟机监控程序未运行"):
		return "Hyper-V 虚拟机监控程序未运行，请确认 BIOS 已开启虚拟化（VT-x/AMD-V）、Hyper-V 功能已启用并重启电脑。原始错误：" + msg
	case strings.Contains(msg, "0x8007000E") || strings.Contains(msg, "内存不足") || strings.Contains(strings.ToLower(msg), "not enough memory"):
		return "主机可用内存不足，无法启动虚拟机。请关闭其他程序或在高级设置中降低内存后重试。原始错误：" + msg
	}
	return msg
}
