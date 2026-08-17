package message

import "wa-shared/wa"

// The codes a consumer can ever see in last_error or a message.failed webhook. Deliberately
// fewer than wa-gateway's: a consumer cannot act on the difference between an upstream timeout
// and an internal error, only on whether resending will help.
const (
	FailureNotOnWhatsApp   = "not_on_whatsapp"
	FailureSenderLoggedOut = "sender_logged_out"
	FailureRateLimited     = "rate_limited_by_whatsapp"
	FailureSendFailed      = "send_failed"
	FailureMessageRejected = "message_rejected"
)

// FailureCode maps a stored wa-gateway error code to the public vocabulary. Anything
// unrecognised becomes send_failed, the honest summary of "we tried and stopped".
func FailureCode(stored string) string {
	switch stored {
	case wa.CodeNotOnWhatsApp:
		return FailureNotOnWhatsApp
	case wa.CodeSessionLoggedOut:
		return FailureSenderLoggedOut
	case wa.CodeRateLimitedByWA:
		return FailureRateLimited
	case wa.CodeMessageRejected:
		return FailureMessageRejected
	default:
		return FailureSendFailed
	}
}

// FailureDetail is the prose that goes with a failure: what happened, and what to do about it.
func FailureDetail(stored, to string) string {
	switch FailureCode(stored) {
	case FailureNotOnWhatsApp:
		return to + " has no WhatsApp account. Not retried — this fails identically forever. Check the number on file for this employee."
	case FailureSenderLoggedOut:
		return "The sender was unlinked from WhatsApp mid-flight. A human has to re-pair it; resend afterwards under a new idempotency key."
	case FailureRateLimited:
		return "WhatsApp pushed back on this sender and the retries ran out. Reduce this sender's volume before resending."
	case FailureMessageRejected:
		return "WhatsApp accepted this message and then rejected it — most often a first message to " + to +
			" without the privacy token an established chat carries. Have " + to +
			" message this sender first, or send from a number with prior history with them, then resend under a new idempotency key."
	default:
		return "The send was attempted and did not succeed before the retries ran out. Safe to resend under a new idempotency key."
	}
}
