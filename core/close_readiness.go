package core

// closeReadinessVerdict is the advisory outcome of closeReadiness. It NEVER
// gates the 🔒封信 button — see closeReadiness's doc comment below.
type closeReadinessVerdict string

const (
	closeReadinessClosed           closeReadinessVerdict = "closed"
	closeReadinessNeedsDirection   closeReadinessVerdict = "needs_direction"
	closeReadinessReadyWithChanges closeReadinessVerdict = "ready_with_changes"
	closeReadinessReady            closeReadinessVerdict = "ready"
	closeReadinessUnknown          closeReadinessVerdict = "unknown"
)

// closeReadiness is the sole definition site for the CLOSED-readiness
// advisory rendered on a receipt inbox card (L-0694 Option A, following up
// on L-0693's finding that the protocol defines who may close a letter and
// what closing does, but never the predicate for when). It is a pure
// function of receiptRecord — no Archive/INDEX read, no I/O, no other
// inputs — and answers only the MECHANICAL part of that predicate: current
// Status, whether it is already closed, whether it was updated since
// arrival. It does NOT and cannot answer the substantive part — whether
// this letter's Expected Output actually landed, or whether Boss has given
// STUCK/BLOCKED a disposition — that judgment stays Boss's alone; this
// function must never be extended to read letter bodies or INDEX rows to
// try to answer it.
//
// The five-row truth table below is evaluated in order; the first matching
// row wins:
//
//  1. ClosedAt set                          -> closed (terminal; caller renders no advisory)
//  2. Status == STUCK or BLOCKED            -> needs_direction
//  3. Status == DONE, Update has sections   -> ready_with_changes
//  4. Status == DONE                        -> ready
//  5. anything else (empty/unrecognized)    -> unknown
//
// Any other view/card-rendering code that re-derives this verdict from
// record.Status/record.ClosedAt/record.Update directly, instead of calling
// this function, reproduces the exact defect shape L-0690 diagnosed in
// resonova's QueueManager.updateSkipMusicButton — the same predicate
// re-implemented ad hoc at a second call site, each version wrong in its
// own way. See TestCloseReadinessSingleDefinitionSite in
// close_readiness_test.go for the boundary gate that gives this invariant a
// red/green signal.
func closeReadiness(record receiptRecord) closeReadinessVerdict {
	switch {
	case record.ClosedAt != "":
		return closeReadinessClosed
	case record.Status == "STUCK" || record.Status == "BLOCKED":
		return closeReadinessNeedsDirection
	case record.Status == "DONE" && len(record.Update.Sections) > 0:
		return closeReadinessReadyWithChanges
	case record.Status == "DONE":
		return closeReadinessReady
	default:
		return closeReadinessUnknown
	}
}

// closeReadinessLine renders closeReadiness's verdict as one advisory line
// of card text, in the caller's i18n language. It returns "" for the closed
// verdict — the terminal card (formatClosedReceiptCard) is a different,
// button-less render path and never shows this line at all.
//
// Text is deliberately advisory ("建议可封信"), never assertive ("可封信"):
// this line reports the mechanical checks passed, not that Boss's
// substantive judgment has been made for them. It carries no bearing on
// button visibility — formatReceiptInboxCard's 🔒封信 button renders
// unconditionally regardless of this line's verdict (L-0694 A-1 constraint
// 2: advisory never gates).
func closeReadinessLine(i18n *I18n, record receiptRecord) string {
	switch closeReadiness(record) {
	case closeReadinessNeedsDirection:
		return i18n.Tf(MsgCloseReadinessNeedsDirection, record.Status)
	case closeReadinessReadyWithChanges:
		return i18n.T(MsgCloseReadinessReadyWithChanges)
	case closeReadinessReady:
		return i18n.T(MsgCloseReadinessReady)
	case closeReadinessUnknown:
		return i18n.T(MsgCloseReadinessUnknown)
	default: // closeReadinessClosed
		return ""
	}
}
