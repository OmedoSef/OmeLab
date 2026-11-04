package lab

import (
	"os"
	"syscall"
)

func statUID(st os.FileInfo) uint32 {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return s.Uid
	}
	return ^uint32(0)
}
