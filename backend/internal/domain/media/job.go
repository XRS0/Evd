package media

// JobType describes the kind of conversion.
type JobType string

const (
	JobHLS JobType = "hls"
	JobMP4 JobType = "mp4"
)

// JobState describes conversion status.
type JobState string

const (
	StateIdle       JobState = "idle"
	StateProcessing JobState = "processing"
	StateReady      JobState = "ready"
	StateFailed     JobState = "failed"
)

// JobStatus is the DTO used by application layer.
type JobStatus struct {
	State      JobState
	URL        string
	Ready      bool
	Processing bool
	Segments   int
	Error      string
	Progress   int
}
