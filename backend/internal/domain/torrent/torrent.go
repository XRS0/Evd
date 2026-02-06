package torrent

// File describes a media file inside torrent payload.
type File struct {
	Index          int    `json:"index"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	Size           int64  `json:"size"`
	BytesCompleted int64  `json:"bytesCompleted"`
	Progress       int    `json:"progress"`
	Streamable     bool   `json:"streamable"`
}

// Info describes a torrent with aggregate transfer and file-level state.
type Info struct {
	ID             int     `json:"id"`
	Name           string  `json:"name"`
	Status         string  `json:"status"`
	PercentDone    float64 `json:"percentDone"`
	Progress       int     `json:"progress"`
	RateDownload   int64   `json:"rateDownload"`
	ETA            int     `json:"eta"`
	SizeWhenDone   int64   `json:"sizeWhenDone"`
	DownloadedEver int64   `json:"downloadedEver"`
	AddedDate      int64   `json:"addedDate"`
	IsFinished     bool    `json:"isFinished"`
	Files          []File  `json:"files"`
}
