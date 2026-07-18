package worker

// DocumentOutcome describes the visible state of a document action without
// exposing any credential or routing information.
type DocumentOutcome struct {
	URL                 string
	ContentWritten      bool
	AnnouncementOutcome string
	// OwnerTransferred is true only when Feishu explicitly confirms that the
	// ingress actor became the owner of this newly-created document.
	OwnerTransferred     bool
	OwnerTransferOutcome string
}
