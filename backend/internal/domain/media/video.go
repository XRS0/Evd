package media

import "time"

// Video represents a source file in the library.
type Video struct {
	Name       string
	Path       string
	Size       int64
	ModifiedAt time.Time
}
