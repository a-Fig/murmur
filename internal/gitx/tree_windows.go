package gitx

import (
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A tree is a job object: git is put in it before it runs, and every process it starts is in it
// too, so ending the job ends them all. Killing git alone, as exec does, leaves its children.
type tree struct{ job windows.Handle }

// basicAccounting is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION, which x/sys doesn't have.
type basicAccounting struct {
	TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
	TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
}

// startTree starts git suspended, so it can start nothing before it is in the job, puts it in a
// new job, and lets it run. A job that can't be made, or git put in it, is gone without: git runs
// as Run's would, and end can end only git.
func startTree(cmd *exec.Cmd) (*tree, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	job, jerr := windows.CreateJobObject(nil, nil)
	if jerr == nil {
		// Closing the job's last handle ends what is in it: should this process die, its fetch dies.
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(job)
			job, jerr = 0, err
		}
	}
	if err := cmd.Start(); err != nil {
		if job != 0 {
			windows.CloseHandle(job)
		}
		return nil, err
	}
	t := &tree{}
	pid := uint32(cmd.Process.Pid)
	if job != 0 {
		if p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid); err == nil {
			if windows.AssignProcessToJobObject(job, p) == nil {
				t.job = job
			}
			windows.CloseHandle(p)
		}
		if t.job == 0 {
			windows.CloseHandle(job)
		}
	}
	if err := resume(pid); err != nil {
		cmd.Process.Kill()
		t.end()
		return nil, err
	}
	return t, nil
}

// resume lets a process started suspended run: its one thread, which exec doesn't keep a handle
// to, is found among the system's.
func resume(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	e := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := false
	for err = windows.Thread32First(snap, &e); err == nil; err = windows.Thread32Next(snap, &e) {
		if e.OwnerProcessID != pid {
			continue
		}
		th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, e.ThreadID)
		if err != nil {
			return err
		}
		_, err = windows.ResumeThread(th)
		windows.CloseHandle(th)
		if err != nil {
			return err
		}
		resumed = true
	}
	if !resumed {
		return syscall.ESRCH
	}
	return nil
}

// end ends every process in the job and waits until none is left, for at most treeGone.
func (t *tree) end() {
	if t.job == 0 {
		return
	}
	defer windows.CloseHandle(t.job)
	windows.TerminateJobObject(t.job, 1)
	deadline := time.Now().Add(treeGone)
	for time.Now().Before(deadline) {
		var a basicAccounting
		if windows.QueryInformationJobObject(t.job, windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&a)), uint32(unsafe.Sizeof(a)), nil) != nil || a.ActiveProcesses == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
