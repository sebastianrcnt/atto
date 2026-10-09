//go:build !race && !darwin && !linux

package server

func maxRSS() uint64 { return 0 }
