//go:build !darwin && !linux && !windows

package system

import "context"

func Measure(context.Context) Sample {
	return Sample{CPUErr: ErrUnsupported, MemErr: ErrUnsupported, BatteryErr: ErrUnsupported}
}
