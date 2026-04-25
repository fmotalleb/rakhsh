package telegram

import "time"

type Progress struct {
	Downloaded int64
	Total      int64

	CurrentSpeed float64 // bytes/sec
	AvgSpeed     float64 // bytes/sec
	Elapsed      time.Duration
	ETA          time.Duration
}
