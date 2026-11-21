package orchestrator

import "time"

const (
	CandidateClaim   = "message.claim"
	CandidateAck     = "message.ack"
	CandidateReply   = "message.reply"
	CandidateReceipt = "message.receipt"
)

// CandidateMessage is delivered under a bounded lease. A recall advances the
// revision of the source, so an acknowledgement for its old body cannot win.
type CandidateMessage struct {
	Run, Source, Lease, Project string
	Revision                    int
	Expires, LeaseExpires       time.Time
	Event                       InboundEvent
}
