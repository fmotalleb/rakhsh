package telegram

import "github.com/gotd/td/tg"

func pickBestPhotoThumb(sizes []tg.PhotoSizeClass) string {
	if len(sizes) == 0 {
		return ""
	}
	bestType := sizes[0].GetType()
	bestScore := -1
	for _, size := range sizes {
		score := 1
		switch s := size.(type) {
		case *tg.PhotoSize:
			score = s.W * s.H
		case *tg.PhotoSizeProgressive:
			score = s.W * s.H
		}
		if score >= bestScore {
			bestScore = score
			bestType = size.GetType()
		}
	}
	return bestType
}
