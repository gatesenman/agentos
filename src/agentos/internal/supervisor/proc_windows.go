//go:build windows

package supervisor

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sysProc on Windows: every agent runs in its own Job Object with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, so the whole tree dies on kill and
// when the supervisor (holder of the job handle) exits. The process is
// created suspended and only resumed after it was assigned to the job, so
// no grandchild can escape. Graceful stop sends CTRL_BREAK_EVENT to the
// agent's console process group (CREATE_NEW_PROCESS_GROUP).
type sysProc struct {
	mu       sync.Mutex
	job      windows.Handle
	pid      uint32
	warnings []string
}

var (
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	user32               = windows.NewLazySystemDLL("user32.dll")
	procAllocConsole     = kernel32.NewProc("AllocConsole")
	procGetConsoleWindow = kernel32.NewProc("GetConsoleWindow")
	procShowWindow       = user32.NewProc("ShowWindow")

	consoleOnce sync.Once
	haveConsole bool
)

// ensureConsole makes sure this process owns a console that agents inherit,
// which is required for GenerateConsoleCtrlEvent. A service or GUI process
// has none, so a hidden one is allocated once.
func ensureConsole() bool {
	consoleOnce.Do(func() {
		if w, _, _ := procGetConsoleWindow.Call(); w != 0 {
			haveConsole = true
			return
		}
		if r, _, _ := procAllocConsole.Call(); r != 0 {
			if w, _, _ := procGetConsoleWindow.Call(); w != 0 {
				procShowWindow.Call(w, windows.SW_HIDE)
			}
			haveConsole = true
		}
		// Otherwise CTRL_BREAK is unavailable: Stop falls back to killing the job.
	})
	return haveConsole
}

const (
	cpuRateControlEnable  = 0x1
	cpuRateControlHardCap = 0x4
)

type jobCPURateControl struct {
	ControlFlags uint32
	CPURate      uint32 // 1/100 percent of the whole machine
}

func prepare(cmd *exec.Cmd, lim limits) (*sysProc, error) {
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED)
	if !ensureConsole() {
		flags |= windows.CREATE_NO_WINDOW
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	sp := &sysProc{job: job}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
		windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
	if lim.memBytes > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		info.JobMemoryLimit = uintptr(lim.memBytes)
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	if lim.cpuPct > 0 {
		rate := lim.cpuPct * 100 / runtime.NumCPU()
		if rate < 1 {
			rate = 1
		}
		if rate > 10000 {
			rate = 10000
		}
		cr := jobCPURateControl{ControlFlags: cpuRateControlEnable | cpuRateControlHardCap, CPURate: uint32(rate)}
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectCpuRateControlInformation,
			uintptr(unsafe.Pointer(&cr)), uint32(unsafe.Sizeof(cr))); err != nil {
			sp.warnings = append(sp.warnings, "CPU 限制设置失败 / cannot apply CPU limit: "+err.Error())
		}
	}
	return sp, nil
}

// started assigns the suspended process to the job, then resumes it.
func (sp *sysProc) started(cmd *exec.Cmd) error {
	sp.pid = uint32(cmd.Process.Pid)
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, sp.pid)
	if err != nil {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	err = windows.AssignProcessToJobObject(sp.job, h)
	windows.CloseHandle(h)
	if err != nil {
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return resumeProcess(sp.pid)
}

func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)
	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	resumed := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if err != nil {
			continue
		}
		if _, err := windows.ResumeThread(th); err == nil {
			resumed++
		}
		windows.CloseHandle(th)
	}
	if resumed == 0 {
		return errors.New("could not resume the main thread")
	}
	return nil
}

func (sp *sysProc) release() {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.job != 0 {
		windows.CloseHandle(sp.job) // KILL_ON_JOB_CLOSE
		sp.job = 0
	}
}

// terminate sends CTRL_BREAK_EVENT to the agent's process group.
func (sp *sysProc) terminate(cmd *exec.Cmd) error {
	if !haveConsole {
		return errors.New("no console for CTRL_BREAK")
	}
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, sp.pid)
}

// kill terminates every process in the job.
func (sp *sysProc) kill(cmd *exec.Cmd) {
	sp.mu.Lock()
	job := sp.job
	if job != 0 {
		windows.TerminateJobObject(job, 1)
	}
	sp.mu.Unlock()
	if cmd.Process != nil {
		cmd.Process.Kill() // also covers a leader that never made it into the job
	}
}

// cleanup runs after the leader exited: kill leftovers and close the job.
func (sp *sysProc) cleanup(cmd *exec.Cmd) {
	sp.mu.Lock()
	if sp.job != 0 {
		windows.TerminateJobObject(sp.job, 1)
	}
	sp.mu.Unlock()
	sp.release()
}
