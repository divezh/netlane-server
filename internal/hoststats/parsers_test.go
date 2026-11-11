package hoststats

import "testing"

func TestProcParsers(t *testing.T) {
	total, idle, ok := parseCPU("cpu 10 20 30 40 5 6 7 8 99 99\ncpu0 1 2")
	if !ok || total != 126 || idle != 45 {
		t.Fatal(total, idle, ok)
	}
	used, all := parseMemory("MemTotal: 2048 kB\nMemAvailable: 512 kB\n")
	if used == nil || *used != 1536*1024 || *all != 2048*1024 {
		t.Fatal(used, all)
	}
	if a, _ := parseMemory("MemTotal: 1 kB"); a != nil {
		t.Fatal("missing availability claimed zero")
	}
}
