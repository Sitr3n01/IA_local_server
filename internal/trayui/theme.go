package trayui

// Color is 0xAARRGGBB, the layout GDI+ takes.
type Color uint32

// COLORREF returns the colour without alpha in GDI's 0x00BBGGRR layout.
func (c Color) COLORREF() uint32 {
	r := uint32(c>>16) & 0xff
	g := uint32(c>>8) & 0xff
	b := uint32(c) & 0xff
	return r | g<<8 | b<<16
}

// WithAlpha replaces the alpha channel; alpha is 0-255.
func (c Color) WithAlpha(alpha uint8) Color {
	return Color(uint32(c)&0x00ffffff | uint32(alpha)<<24)
}

// Blend mixes over onto an opaque base by over's alpha and returns an opaque
// colour. GDI text needs one: ClearType cannot draw on a translucent brush.
func Blend(base, over Color) Color {
	alpha := uint32(over >> 24)
	mix := func(shift uint) uint32 {
		b := uint32(base>>shift) & 0xff
		o := uint32(over>>shift) & 0xff
		return (o*alpha + b*(255-alpha) + 127) / 255
	}
	return Color(0xff000000 | mix(16)<<16 | mix(8)<<8 | mix(0))
}

// ToneColors is one tone of the palette: the solid colour, the colour its
// text takes, and the tint behind it.
type ToneColors struct {
	Solid Color
	Ink   Color
	Tint  Color
}

// Palette mirrors internal/monitor/web/assets/tokens.css, so the flyout and
// the page are one design. Change them together.
type Palette struct {
	Dark         bool
	Bg           Color
	Surface      Color
	Surface2     Color
	Line         Color
	LineSoft     Color
	Ink          Color
	InkSoft      Color
	InkMuted     Color
	AccentFill   Color
	OnAccentFill Color
	Focus        Color
	Accent       ToneColors
	Info         ToneColors
	Warn         ToneColors
	Danger       ToneColors
	Muted        ToneColors
}

// Brand is the icon tile's green in both themes, as in the page's icon.svg.
const Brand Color = 0xff10b981

var LightPalette = Palette{
	Bg:           0xfff9fafb,
	Surface:      0xffffffff,
	Surface2:     0xfff5f6f8,
	Line:         0xffdee5ee,
	LineSoft:     0xffebeff4,
	Ink:          0xff221e1f,
	InkSoft:      0xff4a4546,
	InkMuted:     0xff6b6b6b,
	AccentFill:   0xff047857,
	OnAccentFill: 0xffffffff,
	Focus:        0x590f97ff,
	Accent:       ToneColors{Solid: 0xff10b981, Ink: 0xff047857, Tint: 0x1f10b981},
	Info:         ToneColors{Solid: 0xff0f97ff, Ink: 0xff0a67be, Tint: 0x1f0f97ff},
	Warn:         ToneColors{Solid: 0xffe39b0b, Ink: 0xff9a5b05, Tint: 0x24e39b0b},
	Danger:       ToneColors{Solid: 0xffe0283f, Ink: 0xffbf1d33, Tint: 0x1fe0283f},
	Muted:        ToneColors{Solid: 0xff6b6b6b, Ink: 0xff4a4546, Tint: 0x1f6b6b6b},
}

var DarkPalette = Palette{
	Dark:         true,
	Bg:           0xff0e1113,
	Surface:      0xff161a1d,
	Surface2:     0xff1d2226,
	Line:         0xff2a3036,
	LineSoft:     0xff22272c,
	Ink:          0xffeef1f3,
	InkSoft:      0xffc7cdd3,
	InkMuted:     0xff8e979f,
	AccentFill:   0xff34d399,
	OnAccentFill: 0xff052e22,
	Focus:        0x663aa9ff,
	Accent:       ToneColors{Solid: 0xff34d399, Ink: 0xff34d399, Tint: 0x1f34d399},
	Info:         ToneColors{Solid: 0xff3aa9ff, Ink: 0xff3aa9ff, Tint: 0x1f3aa9ff},
	Warn:         ToneColors{Solid: 0xfff5b53d, Ink: 0xfff5b53d, Tint: 0x1ff5b53d},
	Danger:       ToneColors{Solid: 0xffff5a6e, Ink: 0xffff5a6e, Tint: 0x1fff5a6e},
	Muted:        ToneColors{Solid: 0xff8e979f, Ink: 0xffc7cdd3, Tint: 0x248e979f},
}

// Tone returns the colours of one tone; an unknown tone is muted.
func (p Palette) Tone(tone Tone) ToneColors {
	switch tone {
	case ToneAccent:
		return p.Accent
	case ToneInfo:
		return p.Info
	case ToneWarn:
		return p.Warn
	case ToneDanger:
		return p.Danger
	default:
		return p.Muted
	}
}

// IconColor is the tile colour of the notification-area icon for a tone. The
// icon sits on the taskbar, not on the flyout, so it keeps the light theme's
// saturated colours on both taskbars.
func IconColor(tone Tone) Color {
	switch tone {
	case ToneInfo:
		return LightPalette.Info.Solid
	case ToneWarn:
		return LightPalette.Warn.Solid
	case ToneDanger:
		return LightPalette.Danger.Solid
	case ToneMuted:
		return LightPalette.Muted.Solid
	default:
		return Brand
	}
}
