package matter

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Matter TLV, enough of it to read an operational certificate.
//
// The encoding is specified in the Matter core spec §A.7. Values are
// little-endian, containers are terminated by an end-of-container control byte
// rather than a length, and every element carries a tag whose width is given by
// the top three bits of the control byte. Certificates use only anonymous and
// context-specific tags, but the wider forms are skipped correctly so an
// unexpected one truncates nothing.

const (
	tlvInt8   = 0x00
	tlvInt16  = 0x01
	tlvInt32  = 0x02
	tlvInt64  = 0x03
	tlvUint8  = 0x04
	tlvUint16 = 0x05
	tlvUint32 = 0x06
	tlvUint64 = 0x07
	tlvFalse  = 0x08
	tlvTrue   = 0x09
	tlvFloat  = 0x0a
	tlvDouble = 0x0b
	tlvUTF8_1 = 0x0c
	tlvUTF8_2 = 0x0d
	tlvUTF8_4 = 0x0e
	tlvUTF8_8 = 0x0f
	tlvBytes1 = 0x10
	tlvBytes2 = 0x11
	tlvBytes4 = 0x12
	tlvBytes8 = 0x13
	tlvNull   = 0x14
	tlvStruct = 0x15
	tlvArray  = 0x16
	tlvList   = 0x17
	tlvEnd    = 0x18
)

// tlvMaxDepth bounds recursion. A certificate nests three deep; anything far
// past that is a malformed or hostile file rather than something to follow.
const tlvMaxDepth = 16

var errTLVTruncated = errors.New("truncated TLV")

// tlvElement is one decoded element. Only the fields its type fills are set.
type tlvElement struct {
	tag      uint8
	tagged   bool // a context-specific tag was present
	kind     byte
	num      uint64
	bytes    []byte
	text     string
	children []tlvElement
}

// tlvParse reads elements until the buffer ends or an end-of-container byte is
// reached, returning the elements and the offset just past the terminator.
func tlvParse(buf []byte, at, depth int) ([]tlvElement, int, error) {
	if depth > tlvMaxDepth {
		return nil, 0, errors.New("TLV nested too deeply")
	}
	var out []tlvElement
	for at < len(buf) {
		control := buf[at]
		if control == tlvEnd {
			return out, at + 1, nil
		}
		at++
		element := tlvElement{kind: control & 0x1f}
		switch control >> 5 {
		case 0: // anonymous
		case 1: // context-specific, one byte
			if at >= len(buf) {
				return nil, 0, errTLVTruncated
			}
			element.tag, element.tagged = buf[at], true
			at++
		case 2, 4: // common or implicit profile, two bytes
			at += 2
		case 3, 5: // common or implicit profile, four bytes
			at += 4
		case 6: // fully qualified, six bytes
			at += 6
		case 7: // fully qualified, eight bytes
			at += 8
		}
		if at > len(buf) {
			return nil, 0, errTLVTruncated
		}
		var err error
		if at, err = tlvValue(buf, at, depth, &element); err != nil {
			return nil, 0, err
		}
		out = append(out, element)
	}
	return out, at, nil
}

func tlvValue(buf []byte, at, depth int, element *tlvElement) (int, error) {
	width := func(n int) (int, error) {
		if at+n > len(buf) {
			return 0, errTLVTruncated
		}
		return at + n, nil
	}
	switch element.kind {
	case tlvInt8, tlvUint8:
		end, err := width(1)
		if err != nil {
			return 0, err
		}
		element.num = uint64(buf[at])
		return end, nil
	case tlvInt16, tlvUint16:
		end, err := width(2)
		if err != nil {
			return 0, err
		}
		element.num = uint64(binary.LittleEndian.Uint16(buf[at:end]))
		return end, nil
	case tlvInt32, tlvUint32, tlvFloat:
		end, err := width(4)
		if err != nil {
			return 0, err
		}
		element.num = uint64(binary.LittleEndian.Uint32(buf[at:end]))
		return end, nil
	case tlvInt64, tlvUint64, tlvDouble:
		end, err := width(8)
		if err != nil {
			return 0, err
		}
		element.num = binary.LittleEndian.Uint64(buf[at:end])
		return end, nil
	case tlvFalse, tlvTrue:
		element.num = uint64(element.kind - tlvFalse)
		return at, nil
	case tlvNull:
		return at, nil
	case tlvUTF8_1, tlvUTF8_2, tlvUTF8_4, tlvUTF8_8, tlvBytes1, tlvBytes2, tlvBytes4, tlvBytes8:
		return tlvBlob(buf, at, element)
	case tlvStruct, tlvArray, tlvList:
		children, end, err := tlvParse(buf, at, depth+1)
		if err != nil {
			return 0, err
		}
		element.children = children
		return end, nil
	default:
		return 0, fmt.Errorf("unsupported TLV type 0x%02x", element.kind)
	}
}

// tlvBlob reads a length-prefixed string or byte string. The length field is
// 1, 2, 4 or 8 bytes wide depending on the type, which is what the low two bits
// of these type codes encode.
func tlvBlob(buf []byte, at int, element *tlvElement) (int, error) {
	lengthWidth := 1 << (element.kind & 0x03)
	if at+lengthWidth > len(buf) {
		return 0, errTLVTruncated
	}
	var length uint64
	for i := lengthWidth - 1; i >= 0; i-- {
		length = length<<8 | uint64(buf[at+i])
	}
	at += lengthWidth
	// The cast is guarded: a hostile 8-byte length would otherwise overflow int
	// on a 32-bit build and index a short slice as if it were long.
	if length > uint64(len(buf)-at) {
		return 0, errTLVTruncated
	}
	end := at + int(length)
	if element.kind >= tlvBytes1 {
		element.bytes = buf[at:end]
	} else {
		element.text = string(buf[at:end])
	}
	return end, nil
}

// tlvField returns the first child carrying the given context-specific tag.
func tlvField(elements []tlvElement, tag uint8) (tlvElement, bool) {
	for _, element := range elements {
		if element.tagged && element.tag == tag {
			return element, true
		}
	}
	return tlvElement{}, false
}

// tlvDocument parses a buffer holding one anonymous top-level element and
// returns that element's children, which is the shape of every certificate.
func tlvDocument(buf []byte) ([]tlvElement, error) {
	elements, _, err := tlvParse(buf, 0, 0)
	if err != nil {
		return nil, err
	}
	if len(elements) != 1 || elements[0].children == nil {
		return nil, errors.New("expected a single TLV container")
	}
	return elements[0].children, nil
}
