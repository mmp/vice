package whisper

import (
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	whisperlow "github.com/mmp/vice/autowhisper/internal/whisperlow"
)

type context struct {
	n      int
	model  *model
	params whisperlow.Params
}

var _ Context = (*context)(nil)

func newContext(m *model, p whisperlow.Params) (Context, error) {
	c := new(context)
	c.model = m
	c.params = p
	return c, nil
}

func (c *context) SetLanguage(lang string) error {
	if c.model.ctx == nil {
		return ErrInternalAppError
	}
	if !c.model.IsMultilingual() {
		return ErrModelNotMultilingual
	}
	if lang == "auto" {
		c.params.SetLanguage(-1)
	} else if id := c.model.ctx.Whisper_lang_id(lang); id < 0 {
		return ErrUnsupportedLanguage
	} else if err := c.params.SetLanguage(id); err != nil {
		return err
	}
	return nil
}

func (c *context) IsMultilingual() bool { return c.model.IsMultilingual() }

func (c *context) Language() string {
	id := c.params.Language()
	if id == -1 {
		return "auto"
	}
	return whisperlow.Whisper_lang_str(c.params.Language())
}

func (c *context) DetectedLanguage() string {
	return whisperlow.Whisper_lang_str(c.model.ctx.Whisper_full_lang_id())
}

func (c *context) SetTranslate(v bool)              { c.params.SetTranslate(v) }
func (c *context) SetSplitOnWord(v bool)            { c.params.SetSplitOnWord(v) }
func (c *context) SetThreads(v uint)                { c.params.SetThreads(int(v)) }
func (c *context) SetOffset(v time.Duration)        { c.params.SetOffset(int(v.Milliseconds())) }
func (c *context) SetDuration(v time.Duration)      { c.params.SetDuration(int(v.Milliseconds())) }
func (c *context) SetTokenThreshold(t float32)      { c.params.SetTokenThreshold(t) }
func (c *context) SetTokenSumThreshold(t float32)   { c.params.SetTokenSumThreshold(t) }
func (c *context) SetMaxSegmentLength(n uint)       { c.params.SetMaxSegmentLength(int(n)) }
func (c *context) SetTokenTimestamps(b bool)        { c.params.SetTokenTimestamps(b) }
func (c *context) SetMaxTokensPerSegment(n uint)    { c.params.SetMaxTokensPerSegment(int(n)) }
func (c *context) SetAudioCtx(n uint)               { c.params.SetAudioCtx(int(n)) }
func (c *context) SetMaxContext(n int)              { c.params.SetMaxContext(n) }
func (c *context) SetBeamSize(n int)                { c.params.SetBeamSize(n) }
func (c *context) SetEntropyThold(t float32)        { c.params.SetEntropyThold(t) }
func (c *context) SetInitialPrompt(prompt string)   { c.params.SetInitialPrompt(prompt) }
func (c *context) SetTemperature(t float32)         { c.params.SetTemperature(t) }
func (c *context) SetTemperatureFallback(t float32) { c.params.SetTemperatureFallback(t) }

func (c *context) ResetTimings() { c.model.ctx.Whisper_reset_timings() }
func (c *context) PrintTimings() { c.model.ctx.Whisper_print_timings() }
func (c *context) SystemInfo() string {
	return "system_info: n_threads = " +
		strconvI(c.params.Threads()) + " / " + strconvI(runtime.NumCPU()) +
		" | " + whisperlow.Whisper_print_system_info()
}

func (c *context) Process(data []float32, enc EncoderBeginCallback, seg SegmentCallback, prog ProgressCallback) error {
	if c.model.ctx == nil {
		return ErrInternalAppError
	}
	if seg != nil {
		c.params.SetSingleSegment(true)
	}
	if err := c.model.ctx.Whisper_full(c.params, data,
		func() bool {
			if enc != nil {
				return enc()
			}
			return true
		},
		func(new int) {
			if seg != nil {
				num := c.model.ctx.Whisper_full_n_segments()
				s0 := num - new
				for i := s0; i < num; i++ {
					seg(toSegment(c.model.ctx, i))
				}
			}
		}, func(p int) {
			if prog != nil {
				prog(p)
			}
		},
	); err != nil {
		return err
	}
	return nil
}

func (c *context) NextSegment() (Segment, error) {
	if c.model.ctx == nil {
		return Segment{}, ErrInternalAppError
	}
	if c.n >= c.model.ctx.Whisper_full_n_segments() {
		return Segment{}, io.EOF
	}
	res := toSegment(c.model.ctx, c.n)
	c.n++
	return res, nil
}

func (c *context) IsText(t Token) bool {
	switch {
	case c.IsBEG(t), c.IsSOT(t), whisperlow.Token(t.Id) >= c.model.ctx.Whisper_token_eot(), c.IsPREV(t), c.IsSOLM(t), c.IsNOT(t):
		return false
	default:
		return true
	}
}

func (c *context) IsBEG(t Token) bool {
	return whisperlow.Token(t.Id) == c.model.ctx.Whisper_token_beg()
}
func (c *context) IsSOT(t Token) bool {
	return whisperlow.Token(t.Id) == c.model.ctx.Whisper_token_sot()
}
func (c *context) IsEOT(t Token) bool {
	return whisperlow.Token(t.Id) == c.model.ctx.Whisper_token_eot()
}
func (c *context) IsPREV(t Token) bool {
	return whisperlow.Token(t.Id) == c.model.ctx.Whisper_token_prev()
}
func (c *context) IsSOLM(t Token) bool {
	return whisperlow.Token(t.Id) == c.model.ctx.Whisper_token_solm()
}
func (c *context) IsNOT(t Token) bool {
	return whisperlow.Token(t.Id) == c.model.ctx.Whisper_token_not()
}
func (c *context) IsLANG(t Token, lang string) bool {
	if id := c.model.ctx.Whisper_lang_id(lang); id >= 0 {
		return whisperlow.Token(t.Id) == c.model.ctx.Whisper_token_lang(id)
	}
	return false
}

func toSegment(ctx *whisperlow.Context, n int) Segment {
	return Segment{
		Num:    n,
		Text:   strings.TrimSpace(ctx.Whisper_full_get_segment_text(n)),
		Start:  time.Duration(ctx.Whisper_full_get_segment_t0(n)) * time.Millisecond * 10,
		End:    time.Duration(ctx.Whisper_full_get_segment_t1(n)) * time.Millisecond * 10,
		Tokens: toTokens(ctx, n),
	}
}

func toTokens(ctx *whisperlow.Context, n int) []Token {
	res := make([]Token, ctx.Whisper_full_n_tokens(n))
	for i := range res {
		d := ctx.Whisper_full_get_token_data(n, i)
		res[i] = Token{
			Id:    int(ctx.Whisper_full_get_token_id(n, i)),
			Text:  ctx.Whisper_full_get_token_text(n, i),
			P:     ctx.Whisper_full_get_token_p(n, i),
			Start: time.Duration(d.T0()) * time.Millisecond * 10,
			End:   time.Duration(d.T1()) * time.Millisecond * 10,
		}
	}
	return res
}

func strconvI(i int) string { return fmt.Sprintf("%d", i) }
