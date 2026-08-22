package server

import "fmt"

// CEE service states, as reported back to CEE in the hbStatus field of a
// heartbeat reply.
//
// The values are measured, not assumed. libCEPPAPIWrapper.so holds the five
// names in an indexed pointer array (recovered from .rela.dyn relocations, not
// from the order they happen to sit in rodata), and the index IS the value:
//
//	0  CEPP_SERVICE_ONLINE
//	1  CEPP_SERVICE_OFFLINE
//	2  CEPP_SERVICE_UNREGISTER
//	3  CEPP_SERVICE_REREGISTER
//	4  CEPP_SERVICE_UNKNOWN_STATE
//
// CEE logs the value it read as "Response: HB Status: %d - %ls".
const (
	ceppServiceOnline = 0
	ntStatusSuccess   = 0
)

// heartbeatReply is the answer to CEE's <HeartBeatRequest />.
//
// CEE scans the response for the literals `hbStatus=` and `ntStatus=` and
// parses an integer after each. **The separator between the two is not
// established** — the binary yields the two keys and their values but not the
// framing, and no heartbeat has ever been observed on the wire because
// registration never completed before now. `&` is used because the same
// literal group carries `xml=`, which is the urlencoded-form convention.
//
// If CEE misreads this, the symptom is specific and worth knowing: it treats
// the consumer as CEPP_SERVICE_UNKNOWN_STATE or OFFLINE and stops publishing
// to it, while registration itself keeps succeeding. Check a capture for the
// actual reply CEE accepts from a working partner before changing anything
// else.
func heartbeatReply() []byte {
	return []byte(fmt.Sprintf("hbStatus=%d&ntStatus=%d", ceppServiceOnline, ntStatusSuccess))
}
