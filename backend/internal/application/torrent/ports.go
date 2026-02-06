package torrent

import domain "evd/internal/domain/torrent"

// Gateway is an application port for torrent engine operations.
type Gateway interface {
	Enabled() bool
	List() ([]domain.Info, error)
	AddTorrent(metainfo string) error
	SetSequentialDownload(id int, enabled bool) error
}
