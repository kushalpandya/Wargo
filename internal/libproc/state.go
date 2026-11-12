// SPDX-License-Identifier: AGPL-3.0-only

package libproc

// Info is one process as libproc (or, without cgo, sysctl) reports it.
type Info struct {
	PID, PPID, UID, RUID, Nice, Threads int
	State, Name                         string // State: R S T Z I; Name: pbi_name (up to 32 bytes) when task or BSD info is readable, else p_comm (up to 16 bytes)
	StartUs                             uint64 // start time, microseconds since the epoch (pid-reuse check)
	RSS, VMS                            uint64
	CPUNs                               uint64 // user+system time in ns (mach_timebase_info applied)
	CtxSwitches                         uint64
	Task                                bool // task fields (RSS, VMS, CPUNs, Threads, CtxSwitches) were readable
}

// StateLetter maps a BSD p_stat value to the Linux-style state letter.
func StateLetter(status uint32) string {
	switch status {
	case 1: // SIDL
		return "I"
	case 2: // SRUN
		return "R"
	case 3: // SSLEEP
		return "S"
	case 4: // SSTOP
		return "T"
	case 5: // SZOMB
		return "Z"
	}
	return "?"
}
