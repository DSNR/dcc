package media

// The audio codec is G.722 — SB-ADPCM, RTP payload type 9, 16 kHz mono at
// 64 kbit/s. It carries twice the bandwidth of the µ-law it replaced for
// exactly the same bitrate and the same 160-byte frames, which is the whole
// reason it is here. ADR 0005 records why dcc sends it rather than Opus or
// uncompressed PCM.
const (
	// PayloadTypeG722 is G.722's static RTP payload type, fixed by RFC 3551.
	PayloadTypeG722 = 9
	// MimeTypeG722 is the codec's SDP name.
	MimeTypeG722 = "audio/G722"
	// ClockRate is the RTP timestamp clock the codec runs on. G.722 samples
	// at 16 kHz but its RTP clock is 8 kHz — a known wart of RFC 3551
	// (section 4.5.2) that every stack reproduces, so it is deliberately not
	// SampleRate and must not be "fixed" to it.
	ClockRate = 8000
)

// Encoder turns microphone frames into G.722 payloads. Unlike µ-law, G.722
// is adaptive: the quantiser step, the predictor and the filter memory all
// depend on every sample that came before, so one Encoder belongs to one
// Call's send side and its frames only decode in order.
type Encoder struct {
	low, high band
	qmf       analysisQMF
}

// NewEncoder makes an Encoder in G.722's specified reset state.
func NewEncoder() *Encoder {
	e := &Encoder{}
	e.low.reset(32)
	e.high.reset(8)
	return e
}

// Encode writes one frame of 16 kHz PCM into payload — one byte for every
// two samples — and returns the bytes written. len(pcm) must be even and
// payload must have room for len(pcm)/2 bytes.
func (e *Encoder) Encode(pcm []int16, payload []byte) []byte {
	payload = payload[:len(pcm)/2]
	for i := range payload {
		// The QMF splits each pair of samples into one sub-band sample
		// apiece: everything below 4 kHz, and everything above it.
		lowIn, highIn := e.qmf.analyse(pcm[2*i], pcm[2*i+1])
		// Six bits for the low band, two for the high, in one byte.
		payload[i] = byte(e.high.quantise2(highIn)<<6 | e.low.quantise6(lowIn))
	}
	return payload
}

// Decoder is the receive side's mirror of Encoder, and carries the same
// adaptive state: one Decoder belongs to one Call's receive side.
type Decoder struct {
	low, high band
	qmf       synthesisQMF
}

// NewDecoder makes a Decoder in G.722's specified reset state.
func NewDecoder() *Decoder {
	d := &Decoder{}
	d.low.reset(32)
	d.high.reset(8)
	return d
}

// Decode expands one G.722 payload into pcm — two samples for every byte —
// and returns the samples written. pcm must have room for 2*len(payload).
func (d *Decoder) Decode(payload []byte, pcm []int16) []int16 {
	pcm = pcm[:2*len(payload)]
	for i, b := range payload {
		low := d.low.expand6(int32(b) & 0x3F)
		high := d.high.expand2(int32(b) >> 6)
		pcm[2*i], pcm[2*i+1] = d.qmf.synthesise(low, high)
	}
	return pcm
}

// Conceal writes one frame of silence into pcm — what the receive side plays
// where a frame should have been. The adaptive state is left alone: it is
// still the best guess at where the sender's is, and re-deriving it from
// silence would make the frames that do arrive worse.
func (d *Decoder) Conceal(pcm []int16) []int16 {
	clear(pcm)
	return pcm
}

// The quadrature mirror filter pair that splits 16 kHz audio into G.722's two
// 8 kHz sub-bands and puts it back together. qmfTaps is the 24-tap prototype
// from the recommendation, scaled by 8192; it is symmetric, and its even and
// odd taps are the two polyphase branches — the even ones carry the newer
// sample of each pair, the odd ones the older. The taps sum to 8192, so the
// shifts below are exact and the sub-band domain is the 14-bit one the
// sub-band coders are specified in.
var qmfTaps = [24]int32{
	3, -11, -11, 53, 12, -156, 32, 362, -210, -805, 951, 3876,
	3876, 951, -805, -210, 362, 32, -156, 12, 53, -11, -11, 3,
}

// analysisQMF is the send side's filter: 24 samples of history, newest first,
// so that the newest sample sits on the even branch.
type analysisQMF struct {
	x [24]int32
}

// analyse takes two 16 kHz samples, oldest first, and returns one sample of
// each sub-band: everything below 4 kHz, and everything above it.
func (q *analysisQMF) analyse(first, second int16) (low, high int32) {
	copy(q.x[2:], q.x[:22])
	q.x[0], q.x[1] = int32(second), int32(first)
	var even, odd int32
	for i := range 12 {
		even += qmfTaps[2*i] * q.x[2*i]
		odd += qmfTaps[2*i+1] * q.x[2*i+1]
	}
	// The low band is the prototype itself; the high band is the prototype
	// with alternating signs, which is the same filter moved up a band.
	return (even + odd) >> 14, (even - odd) >> 14
}

// synthesisQMF is the receive side's filter. It keeps the two branch signals
// the analysis split the input into — their sum and their difference — rather
// than the sub-bands themselves, because that is what each half of the
// prototype is run over.
type synthesisQMF struct {
	sum, difference [12]int32
}

// synthesise is analyse's inverse: one sample of each sub-band back into the
// two 16 kHz samples they came from, oldest first.
func (q *synthesisQMF) synthesise(low, high int32) (first, second int16) {
	copy(q.sum[1:], q.sum[:11])
	copy(q.difference[1:], q.difference[:11])
	q.sum[0], q.difference[0] = low+high, low-high
	var even, odd int32
	for i := range 12 {
		even += qmfTaps[2*i] * q.difference[i]
		odd += qmfTaps[2*i+1] * q.sum[i]
	}
	return saturate(even >> 11), saturate(odd >> 11)
}

// The quantiser tables G.722 is defined by, in the order the recommendation's
// own block diagrams use them. They are the whole codec: everything else is
// six lines of arithmetic around them.
var (
	// quantiserThresholds (Q6 in G.722) are the low band's decision levels,
	// scaled by the adaptive step size.
	quantiserThresholds = [30]int32{
		0, 35, 72, 110, 150, 190, 233, 276,
		323, 370, 422, 473, 530, 587, 650, 714,
		786, 858, 940, 1023, 1121, 1219, 1339, 1458,
		1612, 1765, 1980, 2195, 2557, 2919,
	}
	// lowCodesNegative and lowCodesPositive (ILN, ILP) turn the interval a
	// sample landed in into the six-bit code that names it. The codes run
	// backwards — the smallest step is the largest code — which is why the
	// reconstruction tables below look inverted.
	lowCodesNegative = [31]int32{
		0, 63, 62, 31, 30, 29, 28, 27,
		26, 25, 24, 23, 22, 21, 20, 19,
		18, 17, 16, 15, 14, 13, 12, 11,
		10, 9, 8, 7, 6, 5, 4,
	}
	lowCodesPositive = [31]int32{
		0, 61, 60, 59, 58, 57, 56, 55,
		54, 53, 52, 51, 50, 49, 48, 47,
		46, 45, 44, 43, 42, 41, 40, 39,
		38, 37, 36, 35, 34, 33, 32,
	}
	// lowLevels4 (QM4) is the four-bit reconstruction the predictor is
	// updated from — the low band feeds its own 48 kbit/s core back, not the
	// six-bit value, so that a 48 kbit/s peer would stay in step.
	lowLevels4 = [16]int32{
		0, -20456, -12896, -8968, -6288, -4240, -2584, -1200,
		20456, 12896, 8968, 6288, 4240, 2584, 1200, 0,
	}
	// lowLevels6 (QM6) is the six-bit reconstruction the receiver plays.
	lowLevels6 = [64]int32{
		-136, -136, -136, -136,
		-24808, -21904, -19008, -16704,
		-14984, -13512, -12280, -11192,
		-10232, -9360, -8576, -7856,
		-7192, -6576, -6000, -5456,
		-4944, -4464, -4008, -3576,
		-3168, -2776, -2400, -2032,
		-1688, -1360, -1040, -728,
		24808, 21904, 19008, 16704,
		14984, 13512, 12280, 11192,
		10232, 9360, 8576, 7856,
		7192, 6576, 6000, 5456,
		4944, 4464, 4008, 3576,
		3168, 2776, 2400, 2032,
		1688, 1360, 1040, 728,
		432, 136, -432, -136,
	}
	// highLevels (QM2) is the high band's two-bit reconstruction, and
	// highCodesNegative/Positive (IHN, IHP) its code assignment.
	highLevels        = [4]int32{-7408, -1616, 7408, 1616}
	highCodesNegative = [3]int32{0, 1, 0}
	highCodesPositive = [3]int32{0, 3, 2}
	// lowStepAdjust (WL) and highStepAdjust (WH) drive the step size from
	// the code that was just sent; lowStepIndex (RL42) and highStepIndex
	// (RH2) pick the entry.
	lowStepAdjust  = [8]int32{-60, -30, 58, 172, 334, 538, 1198, 3042}
	lowStepIndex   = [16]int32{0, 7, 6, 5, 4, 3, 2, 1, 7, 6, 5, 4, 3, 2, 1, 0}
	highStepAdjust = [3]int32{0, -214, 798}
	highStepIndex  = [4]int32{2, 1, 2, 1}
	// stepSizes (ILB) is the logarithmic step-size table both bands scale
	// out of.
	stepSizes = [32]int32{
		2048, 2093, 2139, 2186, 2233, 2282, 2332, 2383,
		2435, 2489, 2543, 2599, 2656, 2714, 2774, 2834,
		2896, 2960, 3025, 3091, 3158, 3228, 3298, 3371,
		3444, 3520, 3597, 3676, 3756, 3838, 3922, 4008,
	}
)

// band is one sub-band's ADPCM state: the adaptive predictor (two poles and
// six zeroes), its history, and the logarithmic step size. Encoder and
// decoder run identical copies of it, which is what keeps them in step
// without anything on the wire saying so.
type band struct {
	s, sp, sz  int32
	r, a, ap   [3]int32
	p          [3]int32
	d, b, bp   [7]int32
	sg         [7]int32
	nb, detail int32
}

// reset puts a band in G.722's specified initial state. detail is the
// starting step size, which differs between the two bands.
func (b *band) reset(detail int32) {
	*b = band{detail: detail}
}

// quantise6 codes one low-band sample: the difference from the prediction,
// quantised against the current step size, then fed back into the predictor
// through its four-bit core.
func (b *band) quantise6(sample int32) int32 {
	difference := saturate32(sample - b.s)
	magnitude := difference
	if difference < 0 {
		magnitude = ^difference
	}
	interval := 30
	for i := 1; i < 30; i++ {
		if magnitude < (quantiserThresholds[i]*b.detail)>>12 {
			interval = i
			break
		}
	}
	code := lowCodesPositive[interval]
	if difference < 0 {
		code = lowCodesNegative[interval]
	}
	b.updateLow(code >> 2)
	return code
}

// expand6 is quantise6's receive-side mirror: the code back into a sample,
// and the same predictor update from the same four-bit core.
func (b *band) expand6(code int32) int32 {
	sample := b.s + (b.detail*lowLevels6[code])>>15
	b.updateLow(code >> 2)
	return limit(sample)
}

// updateLow feeds one four-bit low-band code back into the predictor and
// moves the step size, which is the half of the codec both ends must do
// identically.
func (b *band) updateLow(core int32) {
	d := (b.detail * lowLevels4[core]) >> 15
	b.nb = clampStep((b.nb*127)>>7+lowStepAdjust[lowStepIndex[core]], 18432)
	b.detail = scaleStep(b.nb, 8)
	b.predict(d)
}

// quantise2 codes one high-band sample. Two bits is one decision either side
// of zero, which is all the 16 kbit/s the high band gets pays for.
func (b *band) quantise2(sample int32) int32 {
	difference := saturate32(sample - b.s)
	magnitude := difference
	if difference < 0 {
		magnitude = ^difference
	}
	interval := 1
	if magnitude >= (564*b.detail)>>12 {
		interval = 2
	}
	code := highCodesPositive[interval]
	if difference < 0 {
		code = highCodesNegative[interval]
	}
	b.updateHigh(code)
	return code
}

// expand2 is quantise2's mirror.
func (b *band) expand2(code int32) int32 {
	sample := b.s + (b.detail*highLevels[code])>>15
	b.updateHigh(code)
	return limit(sample)
}

// updateHigh is updateLow for the high band, which differs only in its
// tables and its two extra bits of step-size headroom.
func (b *band) updateHigh(code int32) {
	d := (b.detail * highLevels[code]) >> 15
	b.nb = clampStep((b.nb*127)>>7+highStepAdjust[highStepIndex[code]], 22528)
	b.detail = scaleStep(b.nb, 10)
	b.predict(d)
}

// predict is G.722's block 4: reconstruct, adapt the two pole and six zero
// coefficients, shift the histories along, and produce the prediction the
// next sample is coded against.
func (b *band) predict(d int32) {
	b.d[0] = d
	b.r[0] = saturate32(b.s + d)
	b.p[0] = saturate32(b.sz + d)

	// The second-order pole adaptation, signs only: G.722 adapts on whether
	// the partial reconstruction changed direction, never on how much.
	for i := range 3 {
		b.sg[i] = b.p[i] >> 15
	}
	wd := saturate32(b.a[1] << 2)
	if b.sg[0] == b.sg[1] {
		wd = -wd
	}
	if wd > 32767 {
		wd = 32767
	}
	pole2 := int32(-128)
	if b.sg[0] == b.sg[2] {
		pole2 = 128
	}
	pole2 += wd >> 7
	pole2 += (b.a[2] * 32512) >> 15
	if pole2 > 12288 {
		pole2 = 12288
	} else if pole2 < -12288 {
		pole2 = -12288
	}
	b.ap[2] = pole2

	// The first-order pole, kept inside the stability triangle the
	// recommendation defines in terms of the second.
	b.sg[0], b.sg[1] = b.p[0]>>15, b.p[1]>>15
	pole1 := int32(-192)
	if b.sg[0] == b.sg[1] {
		pole1 = 192
	}
	pole1 = saturate32(pole1 + (b.a[1]*32640)>>15)
	bound := saturate32(15360 - b.ap[2])
	if pole1 > bound {
		pole1 = bound
	} else if pole1 < -bound {
		pole1 = -bound
	}
	b.ap[1] = pole1

	// The six zeroes, adapted the same sign-only way against the residual.
	step := int32(128)
	if d == 0 {
		step = 0
	}
	b.sg[0] = d >> 15
	for i := 1; i < 7; i++ {
		b.sg[i] = b.d[i] >> 15
		move := step
		if b.sg[i] != b.sg[0] {
			move = -step
		}
		b.bp[i] = saturate32(move + (b.b[i]*32640)>>15)
	}

	for i := 6; i > 0; i-- {
		b.d[i] = b.d[i-1]
		b.b[i] = b.bp[i]
	}
	for i := 2; i > 0; i-- {
		b.r[i] = b.r[i-1]
		b.p[i] = b.p[i-1]
		b.a[i] = b.ap[i]
	}

	// The pole section, then the zero section, then the prediction they add
	// up to.
	first := (b.a[1] * saturate32(b.r[1]+b.r[1])) >> 15
	second := (b.a[2] * saturate32(b.r[2]+b.r[2])) >> 15
	b.sp = saturate32(first + second)
	b.sz = 0
	for i := 6; i > 0; i-- {
		b.sz += (b.b[i] * saturate32(b.d[i]+b.d[i])) >> 15
	}
	b.sz = saturate32(b.sz)
	b.s = saturate32(b.sp + b.sz)
}

// clampStep holds a band's step-size accumulator inside the range its table
// is defined over.
func clampStep(nb, max int32) int32 {
	if nb < 0 {
		return 0
	}
	if nb > max {
		return max
	}
	return nb
}

// scaleStep turns the step-size accumulator into the multiplier the
// quantisers scale by. shift is the band's exponent offset.
func scaleStep(nb, shift int32) int32 {
	exponent := shift - (nb >> 11)
	mantissa := stepSizes[(nb>>6)&31]
	if exponent < 0 {
		return (mantissa << -exponent) << 2
	}
	return (mantissa >> exponent) << 2
}

// limit holds a reconstructed sub-band sample inside the 14-bit range the
// QMF expects, so that a loud passage clips rather than wrapping.
func limit(sample int32) int32 {
	if sample > 16383 {
		return 16383
	}
	if sample < -16384 {
		return -16384
	}
	return sample
}

// saturate32 clamps to 16 bits without leaving the codec's working type,
// which is what G.722's arithmetic is specified in terms of.
func saturate32(value int32) int32 {
	if value > 32767 {
		return 32767
	}
	if value < -32768 {
		return -32768
	}
	return value
}

// saturate clamps to a sample.
func saturate(value int32) int16 {
	return int16(saturate32(value))
}
