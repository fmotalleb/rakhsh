package helper

import "fmt"

func HumanBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	const unit = 1024
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}
