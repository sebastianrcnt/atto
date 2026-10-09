package server

import (
	"bytes"
	"encoding/base64"
	"runtime"
	"runtime/pprof"
)

// debugProfiles backs thread/debug: heap profile, goroutine dump and MemStats
// of the runtime process, for any client.
func debugProfiles() any {
	var heap, goroutines bytes.Buffer
	_ = pprof.Lookup("heap").WriteTo(&heap, 0)
	_ = pprof.Lookup("goroutine").WriteTo(&goroutines, 2)
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	return map[string]any{"heap": base64.StdEncoding.EncodeToString(heap.Bytes()), "goroutines": goroutines.String(), "memory": mem}
}
