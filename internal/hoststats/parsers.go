package hoststats

import (
	"strconv"
	"strings"
)

func parseCPU(raw string) (uint64, uint64, bool) {
	lines := strings.SplitN(raw, "\n", 2)
	fields := strings.Fields(lines[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var total, idle uint64
	for i, field := range fields[1:] {
		if i >= 8 {
			break
		}
		v, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return 0, 0, false
		}
		total += v
		if i == 3 || i == 4 {
			idle += v
		}
	}
	return total, idle, true
}
func parseMemory(raw string) (*uint64, *uint64) {
	values := map[string]uint64{}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err == nil {
			values[strings.TrimSuffix(f[0], ":")] = v * 1024
		}
	}
	total, ok := values["MemTotal"]
	available, has := values["MemAvailable"]
	if !ok || !has || available > total {
		return nil, nil
	}
	used := total - available
	return &used, &total
}
