package media

// The audio codec is G.711 µ-law — PCMU, RTP payload type 0, 8 kHz mono. It
// is one byte a sample, no state, no library, and every WebRTC stack already
// speaks it. ADR 0004 records why dcc sends it rather than Opus.
const (
	// PayloadTypePCMU is PCMU's static RTP payload type, fixed by RFC 3551.
	PayloadTypePCMU = 0
	// MimeTypePCMU is the codec's SDP name.
	MimeTypePCMU = "audio/PCMU"
)

// The constants G.711 is defined in terms of: the bias added before the
// segment search, and the largest 14-bit magnitude the encoding can carry.
const (
	ulawBias = 0x84
	ulawClip = 8159
)

// ulawSegments are the upper bounds of µ-law's eight logarithmic segments,
// in the 14-bit domain the encoder works in.
var ulawSegments = [8]int32{0x3F, 0x7F, 0xFF, 0x1FF, 0x3FF, 0x7FF, 0xFFF, 0x1FFF}

// Encode writes one frame of PCM into payload as µ-law — one byte a sample —
// and returns the bytes written. payload must have room for len(pcm) bytes.
func Encode(pcm []int16, payload []byte) []byte {
	payload = payload[:len(pcm)]
	for i, sample := range pcm {
		payload[i] = encodeSample(sample)
	}
	return payload
}

// Decode expands one µ-law payload into pcm and returns the samples written.
// pcm must have room for len(payload) samples.
func Decode(payload []byte, pcm []int16) []int16 {
	pcm = pcm[:len(payload)]
	for i, b := range payload {
		pcm[i] = decodeSample(b)
	}
	return pcm
}

// encodeSample is G.711's linear-to-µ-law: drop to 14 bits, find the segment
// the magnitude falls in, and keep four bits of mantissa within it — a
// logarithmic scale, so quiet samples keep their resolution.
func encodeSample(sample int16) byte {
	value := int32(sample) >> 2
	mask := int32(0xFF)
	if value < 0 {
		value = -value
		mask = 0x7F
	}
	if value > ulawClip {
		value = ulawClip
	}
	value += ulawBias >> 2

	segment := 0
	for segment < len(ulawSegments) && value > ulawSegments[segment] {
		segment++
	}
	if segment >= len(ulawSegments) {
		return byte(0x7F ^ mask)
	}
	return byte((int32(segment)<<4 | (value>>(segment+1))&0x0F) ^ mask)
}

// decodeSample is G.711's µ-law-to-linear, the exact inverse of the segment
// and mantissa encodeSample chose.
func decodeSample(b byte) int16 {
	b = ^b
	value := (int32(b&0x0F) << 3) + ulawBias
	value <<= (int32(b) & 0x70) >> 4
	if b&0x80 != 0 {
		return int16(ulawBias - value)
	}
	return int16(value - ulawBias)
}
