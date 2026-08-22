package server

import (
	"cmp"
	"fmt"
	"strings"
)

// RegistrationConfig describes this consumer to Dell CEE.
//
// CEE does not accept events from a consumer it has not registered. It PUTs
// <RegisterRequest /> to every configured EndPoint and parses the reply into
// its CRegisterResponse object; from that object it takes the partner's
// identity and the per-protocol event filter. Until that succeeds there is no
// registered CEPA partner, and CEE answers every array heartbeat
// status="0x16" — VC_ERROR_CEPP_NOT_FOUND — so the array never publishes at
// all. See registrationResponseXML for where the required shape comes from.
// The toml tags let cmd/cee-exporter bind [cepa] straight to this type. It
// previously kept a field-for-field clone plus a hand-written copy at the call
// site, where adding a field meant editing three places and forgetting the copy
// compiled cleanly while silently ignoring the operator's config.
type RegistrationConfig struct {
	// FriendlyName identifies this consumer to CEE. It should match the
	// partner id in CEE's own EndPoint setting — the `name` in
	// `name@http://host:port` — because CEE indexes registered partners by
	// name (CRegisterResponse::GetIndex).
	FriendlyName string `toml:"friendly_name"`

	// GUID is this consumer's stable identity. CEE rejects a registration
	// without one ("Guid or FriendlyName not specified").
	GUID string `toml:"guid"`

	// Description populates the desc attribute. CEE requires it:
	// "Incomplete XML. Required description not present".
	Description string `toml:"description"`

	// Protocols is the comma-separated protocol list for the Filter element.
	// Codes are 0=CIFS, 1=NFS, 2=FTP, 3=Unknown, read out of CEE's own
	// ProtocolDesc table.
	Protocols string `toml:"protocols"`

	// EventFilter is the EventTypeFilter value: 24 hex digits, three 32-bit
	// words for CEE's three event phases (pre, post-success, post-failure).
	EventFilter string `toml:"event_filter"`

	// Version populates the EndPoint version attribute. Observed values are
	// "1.0" (CEE's own SplunkHEC proxy) and "1.2" (Varonis).
	Version string `toml:"version"`
}

// Registration defaults.
//
// IMPORTANT: the defaults below DO NOT WORK against real CEE. CEE only registers
// consumers whose identity is in a table compiled into libCEPPAPIWrapper.so —
// CGuidStore, keyed by (friendlyName, facility) → GUID, 47 entries. A
// self-generated GUID is refused with "unknown or invalid GUID", no partner is
// registered, and CEE answers every array heartbeat 0x16 CEPP_NOT_FOUND.
//
// To actually receive events you must set FriendlyName and GUID to a row of that
// table whose facility matches the one CEE has enabled, and use the same name as
// the partner id in CEE's EndPoint value. For the Audit facility, verified
// working against PowerStore on 2026-08-22:
//
//	friendly_name = "PeerSoftwareCollector"
//	guid          = "49f4da0f-055f-401c-9f83-a95ce61447f6"
//
// The full table is in cee-worker's docs/cee-partner-allowlist.md. Note these
// are other vendors' registered identities; there is no mechanism for a
// third-party consumer to obtain its own. That is a deliberate choice to make,
// not a default to inherit silently — which is why the default below is left as
// a name CEE will refuse rather than someone else's.
const (
	DefaultFriendlyName = "ceeexporter"
	DefaultGUID         = "bbefd339-b011-4c7a-89a7-1180d5d531c2"
	DefaultDescription  = "cee-exporter CEPA consumer"
	DefaultProtocols    = "0,1"

	// DefaultEventFilter sets all bits of the FIRST of three 32-bit words and
	// leaves the other two zero.
	//
	// This comment described the opposite until 2026-08-22: it argued for
	// setting every bit of all three words to "sidestep the ordering
	// entirely", which is the value that was deliberately removed below. The
	// stale paragraph was left stacked above the replacement, so the constant
	// carried two contradictory explanations and the header line claimed a
	// subscription the value does not request.
	//
	// All bits of the FIRST 32-bit word, the rest zero. Both known-good
	// captures put every significant bit there and leave the other two words
	// zero — CEE's SplunkHEC template (0xFFFFFFFF0000…) and Varonis
	// (0x000F01FE0000… / 0x004F0FFE0000…). The previous all-96-bits value set
	// bits in words no observed consumer ever sets.
	DefaultEventFilter = "0xFFFFFFFF0000000000000000"

	// DefaultVersion matches Varonis's captured RegisterResponse.
	DefaultVersion = "1.2"
)

// withDefaults fills any unset field. A zero RegistrationConfig is therefore
// usable, which keeps the handler constructible in tests without ceremony.
func (c RegistrationConfig) withDefaults() RegistrationConfig {
	c.FriendlyName = cmp.Or(c.FriendlyName, DefaultFriendlyName)
	c.GUID = cmp.Or(c.GUID, DefaultGUID)
	c.Description = cmp.Or(c.Description, DefaultDescription)
	c.Protocols = cmp.Or(c.Protocols, DefaultProtocols)
	c.EventFilter = cmp.Or(c.EventFilter, DefaultEventFilter)
	c.Version = cmp.Or(c.Version, DefaultVersion)
	return c
}

// registrationResponseXML renders the reply to CEE's <RegisterRequest />.
//
// The shape is not inferred. It is CEE's own: the literal below is the
// template CEE 9.2.0.0 carries for its built-in SplunkHEC proxy, recovered
// from libCEPPAPIWrapper.so in the vendored rpm —
//
//	<RegisterResponse>   <EndPoint friendlyName="SplunkHEC"
//	  guid="0fce0c69-ef49-4362-bae9-180ef0bf97c2" version="1.0"
//	  desc="Dell EMC SplunkHEC Proxy" />    <Filter protocol="0,1">
//	  <EventTypeFilter value="0xFFFFFFFF0000000000000000" />
//	  </Filter></RegisterResponse>
//
// and the rules it is validated against come from CEndPoint::Init() in
// libCEPPFilter.so, whose failure messages are:
//
//	Top node is not RegisterResponse. Fail: %d.
//	Incomplete XML. Required Name or FriendlyName not present
//	Incomplete XML. Required description not present
//	Guid or FriendlyName not specified.
//
// Attributes are escaped defensively even though every field is
// operator-supplied and short: an unescaped quote would produce a document
// CEE cannot parse, and CEE reports that as an ordinary registration failure
// with no detail on Windows, where it writes no log at all.
//
// escapeAttr rather than xml.EscapeText, deliberately: EscapeText would work
// here (strings.Builder is an io.Writer), but it emits &#34;/&#39; rather than
// &quot;/&apos; and rewrites TAB/LF/CR as numeric references. All valid XML,
// but different bytes on a wire whose parser is undocumented and has already
// surprised us twice. An earlier version of this comment said EscapeText was
// avoided because "the marshaller reorders and self-closes differently" —
// that conflated EscapeText with Marshal and was wrong.
func (c RegistrationConfig) registrationResponseXML() []byte {
	// No withDefaults() here: NewHandler applies it once and owns the answer to
	// "who defaults". Two owners is how they drift.
	var b strings.Builder
	b.WriteString("<RegisterResponse>")
	// Attribute set and ordering copied from a RegisterResponse a shipping
	// consumer puts on the wire — Varonis, captured in Dell KB 000049515:
	//
	//	<EndPoint guid="971fbab4-…" friendlyName="Varonis" version="1.2"
	//	          desc="Varonis CEPA event collection Server" />
	//
	// No `name` attribute: CEE's "Required Name or FriendlyName not present"
	// is satisfied by friendlyName alone, and a working consumer sends only
	// that.
	fmt.Fprintf(&b, `<EndPoint guid="%s" friendlyName="%s" version="%s" desc="%s" />`,
		escapeAttr(c.GUID), escapeAttr(c.FriendlyName),
		escapeAttr(c.Version), escapeAttr(c.Description))

	// One <Filter> element PER PROTOCOL, not one element carrying a
	// comma-separated list. The same capture shows:
	//
	//	<Filter protocol="0"><EventTypeFilter value="0x000F01FE…"/></Filter>
	//	<Filter protocol="1"><EventTypeFilter value="0x004F0FFE…"/></Filter>
	//
	// which matches CEE's own CRegisterResponse::GetFilterForProtocol(
	// _EventProtocol, int) — it looks a filter up BY protocol, so a single
	// element claiming "0,1" plausibly matches neither. This package sent the
	// comma form until 2026-08-22; it was never confirmed to work.
	for _, proto := range strings.Split(c.Protocols, ",") {
		proto = strings.TrimSpace(proto)
		if proto == "" {
			continue
		}
		fmt.Fprintf(&b, `<Filter protocol="%s"><EventTypeFilter value="%s" /></Filter>`,
			escapeAttr(proto), escapeAttr(c.EventFilter))
	}
	b.WriteString("</RegisterResponse>")
	return []byte(b.String())
}

// escapeAttr escapes the five XML entities. Written out rather than using
// xml.EscapeText because the response is assembled as a string: CEE's parser
// wants the document laid out the way its own template lays it out, and
// encoding/xml's marshaller reorders and self-closes differently.
// attrEscaper is package-level because strings.Replacer builds a 256-entry
// lookup table on construction and is documented safe for concurrent use.
// Building it per call measured 1371 ns and 6872 B against 44 ns and zero
// allocations hoisted, and escapeAttr runs eight times per registration reply.
var attrEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
)

func escapeAttr(s string) string {
	return attrEscaper.Replace(s)
}
