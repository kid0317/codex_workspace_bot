package worker

// DocumentOutcome describes the visible state of a document action without
// exposing any credential or routing information.
type DocumentOutcome struct {
	URL                 string
	ContentWritten      bool
	AnnouncementOutcome string
}
