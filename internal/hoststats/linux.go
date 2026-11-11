//go:build linux

package hoststats

import (
	"os"
	"runtime"
	"syscall"

	"github.com/divezh/netlane-server/internal/store"
)

type Sampler struct {
	total, idle uint64
	ready       bool
}

func (s *Sampler) Read(path string) store.Resources {
	out := store.Resources{Cores: runtime.NumCPU()}
	if raw, err := os.ReadFile("/proc/stat"); err == nil {
		total, idle, ok := parseCPU(string(raw))
		if ok {
			if s.ready && total > s.total && idle >= s.idle && idle-s.idle <= total-s.total {
				v := 100 * (1 - float64(idle-s.idle)/float64(total-s.total))
				out.CPU = &v
			}
			s.total = total
			s.idle = idle
			s.ready = true
		}
	}
	if raw, err := os.ReadFile("/proc/meminfo"); err == nil {
		out.MemoryUsed, out.MemoryTotal = parseMemory(string(raw))
	}
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) == nil {
		total := st.Blocks * uint64(st.Bsize)
		used := (st.Blocks - st.Bfree) * uint64(st.Bsize)
		out.DiskUsed = &used
		out.DiskTotal = &total
	}
	return out
}
