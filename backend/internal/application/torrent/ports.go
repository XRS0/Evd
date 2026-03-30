package torrent

import domain "evd/internal/domain/torrent"

// Gateway is an application port for torrent engine operations.
type Gateway interface {
	Enabled() bool
	List() ([]domain.Info, error)
	AddTorrent(metainfo, displayName string) error
	Start(id int) error
	Stop(id int) error
	SetSequentialDownload(id int, enabled bool) error
	SetStreamingFocus(id, fileIndex int, positionRatio float64) error
}
