package hyperv

import "errors"

var (
	errNotWindows   = errors.New("hyperv: 仅支持 Windows 宿主（需启用 Hyper-V）")
	errNotAvailable = errors.New("hyperv: Hyper-V 不可用（未启用或无权限）")
	errNoISO        = errors.New("hyperv: 缺少安装镜像：请留空以应用内自动下载，或在高级选项填写本机 iso_path")
)
