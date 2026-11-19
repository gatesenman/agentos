//go:build windows

package agentoslocal

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobHandle is a Job Object with KILL_ON_JOB_CLOSE: closing it (or the server
// exiting) terminates the node and every agent it spawned.
type jobHandle struct{ h windows.Handle }

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200 | 0x08000000} // NEW_PROCESS_GROUP | NO_WINDOW
}

func afterStart(cmd *exec.Cmd) jobHandle {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return jobHandle{}
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return jobHandle{}
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err == nil {
		windows.AssignProcessToJobObject(h, ph)
		windows.CloseHandle(ph)
	}
	return jobHandle{h}
}

func closeJob(j jobHandle) {
	if j.h != 0 {
		windows.CloseHandle(j.h)
	}
}

// Windows has no SIGTERM for console-less children: terminate the whole job.
func terminate(cmd *exec.Cmd, j jobHandle) { kill(cmd, j) }

func kill(cmd *exec.Cmd, j jobHandle) {
	if j.h != 0 {
		windows.TerminateJobObject(j.h, 1)
	}
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}

func cleanStale(string) {}
