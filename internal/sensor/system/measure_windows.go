package system

import (
	"context"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	getSystemTimes       = kernel32.NewProc("GetSystemTimes")
	globalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	getSystemPowerStatus = kernel32.NewProc("GetSystemPowerStatus")
)

type memoryStatusEx struct {
	Length, MemoryLoad                                 uint32
	TotalPhys, AvailPhys, TotalPageFile, AvailPageFile uint64
	TotalVirtual, AvailVirtual, AvailExtendedVirtual   uint64
}

type systemPowerStatus struct {
	ACLineStatus, BatteryFlag, BatteryLifePercent, SystemStatusFlag byte
	BatteryLifeTime, BatteryFullLifeTime                            uint32
}

func ft(f syscall.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }

func systemTimes() (total, idle uint64, err error) {
	var i, k, u syscall.Filetime
	if r, _, e := getSystemTimes.Call(uintptr(unsafe.Pointer(&i)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u))); r == 0 {
		return 0, 0, e
	}
	return ft(k) + ft(u), ft(i), nil // kernel time includes idle time
}

// Measure measures on Windows with kernel32. Swap is not measured (null).
func Measure(ctx context.Context) Sample {
	var s Sample
	if t0, i0, err := systemTimes(); err != nil {
		s.CPUErr = err
	} else {
		select {
		case <-ctx.Done():
			s.CPUErr = ctx.Err()
		case <-time.After(time.Second):
			if t1, i1, err := systemTimes(); err != nil {
				s.CPUErr = err
			} else if v, err := cpuBetween(t0, i0, t1, i1); err != nil {
				s.CPUErr = err
			} else {
				s.CPU = &v
			}
		}
	}
	m := memoryStatusEx{}
	m.Length = uint32(unsafe.Sizeof(m))
	if r, _, e := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m))); r == 0 {
		s.MemErr = e
	} else {
		s.MemUsed, s.MemTotal = m.TotalPhys-m.AvailPhys, m.TotalPhys
	}
	var p systemPowerStatus
	if r, _, e := getSystemPowerStatus.Call(uintptr(unsafe.Pointer(&p))); r == 0 {
		s.BatteryErr = e
	} else if p.BatteryFlag&128 == 0 && p.BatteryLifePercent <= 100 {
		pct, charging := float64(p.BatteryLifePercent), p.BatteryFlag&8 != 0
		s.Battery, s.Charging = &pct, &charging
	}
	return s
}
