//go:build !windows

package evtx

// Constants mirrored from go-evtx for test assertions.
// These match the binary layout values used by the EVTX format.
const (
	evtxFileHeaderSize  = 4096
	evtxChunkHeaderSize = 512
	evtxRecordSignature = uint32(0x00002A2A)
	evtxRecordsStart    = uint32(evtxChunkHeaderSize)
)

const evtxChunkMagic = "ElfChnk\x00"
