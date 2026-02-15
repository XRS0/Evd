package transmission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	domainmedia "evd/internal/domain/media"
	"evd/internal/domain/torrent"
	"evd/internal/infrastructure/filesystem"
)

// Client is a Transmission RPC infrastructure adapter.
type Client struct {
	URL         string
	User        string
	Pass        string
	DownloadDir string
	HTTP        *http.Client
	mu          sync.Mutex
	sessionID   string
	focusMode   streamingFocusMode
	lastPiece   map[string]int
	store       *filesystem.Store
}

// NewClient creates a Transmission RPC adapter.
func NewClient(url, user, pass, downloadDir string, store *filesystem.Store) *Client {
	return &Client{
		URL:         strings.TrimSpace(url),
		User:        user,
		Pass:        pass,
		DownloadDir: downloadDir,
		HTTP:        &http.Client{Timeout: 12 * time.Second},
		lastPiece:   map[string]int{},
		store:       store,
	}
}

// Enabled reports whether Transmission integration is configured.
func (c *Client) Enabled() bool {
	return c.URL != ""
}

// List fetches torrent list and maps it into domain objects.
func (c *Client) List() ([]torrent.Info, error) {
	resp, err := c.request("torrent-get", map[string]interface{}{
		"fields": []string{
			"id",
			"name",
			"status",
			"percentDone",
			"rateDownload",
			"eta",
			"sizeWhenDone",
			"downloadedEver",
			"addedDate",
			"isFinished",
			"files",
		},
	})
	if err != nil {
		return nil, err
	}

	var args struct {
		Torrents []struct {
			ID             int     `json:"id"`
			Name           string  `json:"name"`
			Status         int     `json:"status"`
			PercentDone    float64 `json:"percentDone"`
			RateDownload   int64   `json:"rateDownload"`
			ETA            int     `json:"eta"`
			SizeWhenDone   int64   `json:"sizeWhenDone"`
			DownloadedEver int64   `json:"downloadedEver"`
			AddedDate      int64   `json:"addedDate"`
			IsFinished     bool    `json:"isFinished"`
			Files          []struct {
				BytesCompleted int64  `json:"bytesCompleted"`
				Length         int64  `json:"length"`
				Name           string `json:"name"`
			} `json:"files"`
		} `json:"torrents"`
	}
	if err := json.Unmarshal(resp.Arguments, &args); err != nil {
		return nil, err
	}

	items := make([]torrent.Info, 0, len(args.Torrents))
	for _, t := range args.Torrents {
		progress := int(t.PercentDone*100 + 0.5)
		files := make([]torrent.File, 0, len(t.Files))
		for idx, f := range t.Files {
			rel, err := domainmedia.NormalizeVideoPath(f.Name)
			if err != nil {
				continue
			}
			fileProgress := 0
			if f.Length > 0 {
				fileProgress = int(float64(f.BytesCompleted)/float64(f.Length)*100 + 0.5)
			}
			files = append(files, torrent.File{
				Index:          idx,
				Name:           f.Name,
				Path:           rel,
				Size:           f.Length,
				BytesCompleted: f.BytesCompleted,
				Progress:       fileProgress,
				Streamable:     f.BytesCompleted > 0 && c.store.FileExists(rel),
			})
		}
		items = append(items, torrent.Info{
			ID:             t.ID,
			Name:           t.Name,
			Status:         mapStatus(t.Status),
			PercentDone:    t.PercentDone,
			Progress:       progress,
			RateDownload:   t.RateDownload,
			ETA:            t.ETA,
			SizeWhenDone:   t.SizeWhenDone,
			DownloadedEver: t.DownloadedEver,
			AddedDate:      t.AddedDate,
			IsFinished:     t.IsFinished,
			Files:          files,
		})
	}

	return items, nil
}

// AddTorrent adds torrent metadata to Transmission.
func (c *Client) AddTorrent(metainfo string) error {
	_, err := c.request("torrent-add", map[string]interface{}{
		"metainfo":     metainfo,
		"download-dir": c.DownloadDir,
		"paused":       false,
	})
	return err
}

// SetSequentialDownload toggles sequential mode for a torrent.
func (c *Client) SetSequentialDownload(id int, enabled bool) error {
	_, err := c.request("torrent-set", map[string]interface{}{
		"ids":                []int{id},
		"sequentialDownload": enabled,
	})
	return err
}

// SetStreamingFocus nudges torrent download around the current playback position.
// When Transmission doesn't support piece-based focus, this falls back to sequential mode.
func (c *Client) SetStreamingFocus(id, fileIndex int, positionRatio float64) error {
	mode := c.getFocusMode()
	if mode == streamingFocusBasic {
		return c.setBasicFocus(id, fileIndex)
	}

	pieceInfo, err := c.fetchPieceInfo(id, fileIndex)
	if err != nil {
		if isPieceInfoUnsupported(err) {
			c.setFocusMode(streamingFocusBasic)
		}
		return c.setBasicFocus(id, fileIndex)
	}

	startPiece, ok := pieceInfo.startPieceForRatio(positionRatio)
	if !ok {
		return c.setBasicFocus(id, fileIndex)
	}
	if c.sameFocusedPiece(id, fileIndex, startPiece) {
		return nil
	}

	err = c.setAdvancedFocus(id, fileIndex, startPiece)
	if err == nil {
		c.setFocusMode(streamingFocusAdvanced)
		c.rememberPiece(id, fileIndex, startPiece)
		return nil
	}

	if !isUnsupportedFocusError(err) {
		return err
	}

	c.setFocusMode(streamingFocusBasic)
	return c.setBasicFocus(id, fileIndex)
}

type streamingFocusMode uint8

const (
	streamingFocusUnknown streamingFocusMode = iota
	streamingFocusAdvanced
	streamingFocusBasic
)

type pieceInfo struct {
	length     int64
	pieceSize  int64
	beginPiece int
	endPiece   int
}

func (p pieceInfo) startPieceForRatio(ratio float64) (int, bool) {
	if p.length <= 0 || p.pieceSize <= 0 || p.endPiece < p.beginPiece {
		return 0, false
	}

	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}

	offset := int64(float64(p.length-1) * ratio)
	if offset < 0 {
		offset = 0
	}

	pieceOffset := int(offset / p.pieceSize)
	start := p.beginPiece + pieceOffset
	if start < p.beginPiece {
		start = p.beginPiece
	}
	if start > p.endPiece {
		start = p.endPiece
	}
	return start, true
}

func (c *Client) fetchPieceInfo(id, fileIndex int) (pieceInfo, error) {
	resp, err := c.request("torrent-get", map[string]interface{}{
		"ids":    []int{id},
		"fields": []string{"pieceSize", "files"},
	})
	if err != nil {
		return pieceInfo{}, err
	}

	var args struct {
		Torrents []struct {
			PieceSize      int64 `json:"pieceSize"`
			PieceSizeSnake int64 `json:"piece_size"`
			Files          []struct {
				Length          int64 `json:"length"`
				BeginPiece      *int  `json:"beginPiece"`
				BeginPieceSnake *int  `json:"begin_piece"`
				EndPiece        *int  `json:"endPiece"`
				EndPieceSnake   *int  `json:"end_piece"`
			} `json:"files"`
		} `json:"torrents"`
	}
	if err := json.Unmarshal(resp.Arguments, &args); err != nil {
		return pieceInfo{}, err
	}
	if len(args.Torrents) == 0 {
		return pieceInfo{}, errors.New("torrent not found")
	}

	torrentItem := args.Torrents[0]
	if fileIndex >= len(torrentItem.Files) {
		return pieceInfo{}, errors.New("torrent file not found")
	}

	file := torrentItem.Files[fileIndex]
	beginPiece, hasBegin := choosePieceField(file.BeginPiece, file.BeginPieceSnake)
	endPiece, hasEnd := choosePieceField(file.EndPiece, file.EndPieceSnake)
	if !hasBegin || !hasEnd {
		return pieceInfo{}, errors.New("piece boundaries are unavailable")
	}

	pieceSize := torrentItem.PieceSize
	if pieceSize <= 0 {
		pieceSize = torrentItem.PieceSizeSnake
	}

	return pieceInfo{
		length:     file.Length,
		pieceSize:  pieceSize,
		beginPiece: beginPiece,
		endPiece:   endPiece,
	}, nil
}

func choosePieceField(primary, fallback *int) (int, bool) {
	if primary != nil {
		return *primary, true
	}
	if fallback != nil {
		return *fallback, true
	}
	return 0, false
}

func isUnsupportedFocusError(err error) bool {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "sequentialdownloadfrompiece") || strings.Contains(message, "sequential_download_from_piece") {
		return true
	}
	if strings.Contains(message, "priorityhigh") || strings.Contains(message, "priority_high") {
		return true
	}
	return strings.Contains(message, "unknown") && strings.Contains(message, "argument")
}

func isPieceInfoUnsupported(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "piece boundaries are unavailable")
}

func (c *Client) getFocusMode() streamingFocusMode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.focusMode
}

func (c *Client) setFocusMode(mode streamingFocusMode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.focusMode = mode
}

func (c *Client) sameFocusedPiece(id, fileIndex, piece int) bool {
	key := fmt.Sprintf("%d:%d", id, fileIndex)
	c.mu.Lock()
	defer c.mu.Unlock()
	prev, ok := c.lastPiece[key]
	return ok && prev == piece
}

func (c *Client) rememberPiece(id, fileIndex, piece int) {
	key := fmt.Sprintf("%d:%d", id, fileIndex)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastPiece[key] = piece
}

func (c *Client) setBasicFocus(id, fileIndex int) error {
	camelArgs := map[string]interface{}{
		"ids":                []int{id},
		"sequentialDownload": true,
		"priorityHigh":       []int{fileIndex},
	}
	_, err := c.request("torrent-set", camelArgs)
	if err == nil || !isUnsupportedFocusError(err) {
		return err
	}

	snakeArgs := map[string]interface{}{
		"ids":                 []int{id},
		"sequential_download": true,
		"priority_high":       []int{fileIndex},
	}
	_, err = c.request("torrent-set", snakeArgs)
	return err
}

func (c *Client) setAdvancedFocus(id, fileIndex, startPiece int) error {
	camelArgs := map[string]interface{}{
		"ids":                         []int{id},
		"sequentialDownload":          true,
		"sequentialDownloadFromPiece": startPiece,
		"priorityHigh":                []int{fileIndex},
	}
	_, err := c.request("torrent-set", camelArgs)
	if err == nil || !isUnsupportedFocusError(err) {
		return err
	}

	snakeArgs := map[string]interface{}{
		"ids":                            []int{id},
		"sequential_download":            true,
		"sequential_download_from_piece": startPiece,
		"priority_high":                  []int{fileIndex},
	}
	_, err = c.request("torrent-set", snakeArgs)
	return err
}

type response struct {
	Result    string          `json:"result"`
	Arguments json.RawMessage `json:"arguments"`
}

func (c *Client) request(method string, arguments map[string]interface{}) (response, error) {
	if !c.Enabled() {
		return response{}, errors.New("Transmission is not configured")
	}

	payload := map[string]interface{}{
		"method":    method,
		"arguments": arguments,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return response{}, err
	}

	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequest("POST", c.URL, bytes.NewReader(data))
		if err != nil {
			return response{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		if sessionID := c.getSessionID(); sessionID != "" {
			req.Header.Set("X-Transmission-Session-Id", sessionID)
		}
		if c.User != "" || c.Pass != "" {
			req.SetBasicAuth(c.User, c.Pass)
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return response{}, err
		}

		if resp.StatusCode == http.StatusConflict {
			newID := resp.Header.Get("X-Transmission-Session-Id")
			resp.Body.Close()
			if newID == "" {
				return response{}, errors.New("Transmission session id missing")
			}
			c.setSessionID(newID)
			continue
		}

		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			return response{}, fmt.Errorf("Transmission error: %s", strings.TrimSpace(string(body)))
		}

		var out response
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return response{}, err
		}
		if out.Result != "success" {
			return response{}, fmt.Errorf("Transmission error: %s", out.Result)
		}
		return out, nil
	}

	return response{}, errors.New("Transmission session negotiation failed")
}

func mapStatus(code int) string {
	switch code {
	case 0:
		return "stopped"
	case 1:
		return "check_wait"
	case 2:
		return "checking"
	case 3:
		return "download_wait"
	case 4:
		return "downloading"
	case 5:
		return "seed_wait"
	case 6:
		return "seeding"
	default:
		return "unknown"
	}
}

func (c *Client) getSessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

func (c *Client) setSessionID(value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionID = value
}
