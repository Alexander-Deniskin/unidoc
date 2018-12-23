/*
 * This file is subject to the terms and conditions defined in
 * file 'LICENSE.md', which is part of this source code package.
 */

package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/unidoc/unidoc/common"
	"github.com/unidoc/unidoc/pdf/core"
	"github.com/unidoc/unidoc/pdf/internal/cmap"
	"github.com/unidoc/unidoc/pdf/internal/textencoding"
	"github.com/unidoc/unidoc/pdf/model/fonts"
)

// pdfFont is an internal interface for fonts that can be stored in PDF documents.
type pdfFont interface {
	fonts.Font
	// getFontDescriptor returns the font descriptor of the font.
	getFontDescriptor() *PdfFontDescriptor
	// baseFields returns fields that are common for PDF fonts.
	baseFields() *fontCommon
}

// PdfFont represents an underlying font structure which can be of type:
// - Type0
// - Type1
// - TrueType
// etc.
type PdfFont struct {
	context pdfFont // The underlying font: Type0, Type1, Truetype, etc..
}

// GetFontDescriptor returns the font descriptor for `font`.
func (font PdfFont) GetFontDescriptor() (*PdfFontDescriptor, error) {
	return font.context.getFontDescriptor(), nil
}

// String returns a string that describes `font`.
func (font PdfFont) String() string {
	enc := ""
	if font.context.Encoder() != nil {
		enc = font.context.Encoder().String()
	}
	return fmt.Sprintf("FONT{%T %s %s}", font.context, font.baseFields().coreString(), enc)

}

// BaseFont returns the font's "BaseFont" field.
func (font PdfFont) BaseFont() string {
	return font.baseFields().basefont
}

// Subtype returns the font's "Subtype" field.
func (font PdfFont) Subtype() string {
	subtype := font.baseFields().subtype
	if t, ok := font.context.(*pdfFontType0); ok {
		subtype = subtype + ":" + t.DescendantFont.Subtype()
	}
	return subtype
}

// IsCID returns true if the underlying font is CID.
func (font PdfFont) IsCID() bool {
	return font.baseFields().isCIDFont()
}

// ToUnicode returns the name of the font's "ToUnicode" field if there is one, or "" if there isn't.
func (font PdfFont) ToUnicode() string {
	if font.baseFields().toUnicodeCmap == nil {
		return ""
	}
	return font.baseFields().toUnicodeCmap.Name()
}

// DefaultFont returns the default font, which is currently the built in Helvetica.
func DefaultFont() *PdfFont {
	std := stdFontToSimpleFont(fonts.NewFontHelvetica())
	return &PdfFont{context: &std}
}

// NewStandard14Font returns the standard 14 font named `basefont` as a *PdfFont, or an error if it
// `basefont` is not one of the standard 14 font names.
func NewStandard14Font(basefont fonts.StdFontName) (*PdfFont, error) {
	fnt, ok := fonts.NewStdFontByName(basefont)
	if !ok {
		common.Log.Debug("ERROR: Invalid standard 14 font name %#q", basefont)
		return nil, ErrFontNotSupported
	}
	std := stdFontToSimpleFont(fnt)
	return &PdfFont{context: &std}, nil
}

// NewStandard14FontMustCompile returns the standard 14 font named `basefont` as a *PdfFont.
// If `basefont` is one of the 14 Standard14Font values defined above then NewStandard14FontMustCompile
// is guaranteed to succeed.
func NewStandard14FontMustCompile(basefont fonts.StdFontName) *PdfFont {
	font, err := NewStandard14Font(basefont)
	if err != nil {
		panic(fmt.Errorf("invalid Standard14Font %#q", basefont))
	}
	return font
}

// NewStandard14FontWithEncoding returns the standard 14 font named `basefont` as a *PdfFont and an
// a SimpleEncoder that encodes all the runes in `alphabet`, or an error if this is not possible.
// An error can occur if`basefont` is not one the standard 14 font names.
func NewStandard14FontWithEncoding(basefont fonts.StdFontName, alphabet map[rune]int) (*PdfFont, *textencoding.SimpleEncoder, error) {
	baseEncoder := "MacRomanEncoding"
	common.Log.Trace("NewStandard14FontWithEncoding: basefont=%#q baseEncoder=%#q alphabet=%q",
		basefont, baseEncoder, string(sortedAlphabet(alphabet)))

	fnt, ok := fonts.NewStdFontByName(basefont)
	if !ok {
		return nil, nil, ErrFontNotSupported
	}
	std := stdFontToSimpleFont(fnt)

	encoder, err := textencoding.NewSimpleTextEncoder(baseEncoder, nil)
	if err != nil {
		return nil, nil, err
	}

	// glyphCode are the encoding glyphs. We need to match them to the font glyphs.
	glyphCode := make(map[textencoding.GlyphName]textencoding.CharCode)

	// slots are the indexes in the encoding where the new character codes are added.
	// slots are unused indexes, which are filled first. slots1 are the used indexes.
	var slots, slots1 []textencoding.CharCode
	for code := textencoding.CharCode(1); code <= 0xff; code++ {
		if glyph, ok := encoder.CharcodeToGlyph(code); ok {
			glyphCode[glyph] = code
			// Don't overwrite space
			if glyph != "space" {

				slots1 = append(slots1, code)
			}
		} else {
			slots = append(slots, code)
		}
	}
	slots = append(slots, slots1...)

	// `glyphs` are the font glyphs that we need to encode.
	var glyphs []textencoding.GlyphName
	for _, r := range sortedAlphabet(alphabet) {
		glyph, ok := textencoding.RuneToGlyph(r)
		if !ok {
			common.Log.Debug("No glyph for rune 0x%02x=%c", r, r)
			continue
		}
		if _, ok = std.fontMetrics[glyph]; !ok {
			common.Log.Trace("Glyph %q (0x%04x=%c)not in font", glyph, r, r)
			continue
		}
		if len(glyphs) >= 255 {
			common.Log.Debug("Too many characters for encoding")
			break
		}
		glyphs = append(glyphs, glyph)

	}

	// Fill the slots, starting with the empty ones.
	slotIdx := 0
	differences := make(map[textencoding.CharCode]textencoding.GlyphName)
	for _, glyph := range glyphs {
		if _, ok := glyphCode[glyph]; !ok {
			differences[slots[slotIdx]] = glyph
			slotIdx++
		}
	}
	encoder, err = textencoding.NewSimpleTextEncoder(baseEncoder, differences)

	return &PdfFont{context: &std}, encoder, err
}

// GetAlphabet returns a map of the runes in `text`.
func GetAlphabet(text string) map[rune]int {
	alphabet := map[rune]int{}
	for _, r := range text {
		alphabet[r]++
	}
	return alphabet
}

// sortedAlphabet the runes in `alphabet` sorted by frequency.
func sortedAlphabet(alphabet map[rune]int) []rune {
	var runes []rune
	for r := range alphabet {
		runes = append(runes, r)
	}
	sort.Slice(runes, func(i, j int) bool {
		ri, rj := runes[i], runes[j]
		ni, nj := alphabet[ri], alphabet[rj]
		if ni != nj {
			return ni < nj
		}
		return ri < rj
	})
	return runes
}

// NewPdfFontFromPdfObject loads a PdfFont from the dictionary `fontObj`.  If there is a problem an
// error is returned.
func NewPdfFontFromPdfObject(fontObj core.PdfObject) (*PdfFont, error) {
	return newPdfFontFromPdfObject(fontObj, true)
}

// newPdfFontFromPdfObject loads a PdfFont from the dictionary `fontObj`.  If there is a problem an
// error is returned.
// The allowType0 flag indicates whether loading Type0 font should be supported.  This is used to
// avoid cyclical loading.
func newPdfFontFromPdfObject(fontObj core.PdfObject, allowType0 bool) (*PdfFont, error) {
	d, base, err := newFontBaseFieldsFromPdfObject(fontObj)
	if err != nil {
		return nil, err
	}

	font := &PdfFont{}
	switch base.subtype {
	case "Type0":
		if !allowType0 {
			common.Log.Debug("ERROR: Loading type0 not allowed. font=%s", base)
			return nil, errors.New("cyclical type0 loading")
		}
		type0font, err := newPdfFontType0FromPdfObject(d, base)
		if err != nil {
			common.Log.Debug("ERROR: While loading Type0 font. font=%s err=%v", base, err)
			return nil, err
		}
		font.context = type0font
	case "Type1", "Type3", "MMType1", "TrueType":
		var simplefont *pdfFontSimple
		if fnt, ok := fonts.NewStdFontByName(fonts.StdFontName(base.basefont)); ok && base.subtype == "Type1" {
			std := stdFontToSimpleFont(fnt)
			font.context = &std
			simplefont, err = newSimpleFontFromPdfObject(d, base, true)
			if err != nil {
				common.Log.Debug("ERROR: Bad Standard14\n\tfont=%s\n\tstd=%+v", base, std)
				return nil, err
			}
			encdict, has := core.GetDict(simplefont.Encoding)
			if has {
				// Set the default encoding for the standard 14 font if not specified in Encoding.
				if encdict.Get("BaseEncoding") == nil {
					encdict.Set("BaseEncoding", std.encoder.ToPdfObject())
				}
			} else {
				simplefont.encoder = std.encoder
			}

			simplefont.firstChar = 0
			simplefont.lastChar = 255
			simplefont.fontMetrics = std.fontMetrics
		} else {
			simplefont, err = newSimpleFontFromPdfObject(d, base, false)
			if err != nil {
				common.Log.Debug("ERROR: While loading simple font: font=%s err=%v", base, err)
				return nil, err
			}
		}
		err = simplefont.addEncoding()
		if err != nil {
			return nil, err
		}
		font.context = simplefont
	case "CIDFontType0":
		cidfont, err := newPdfCIDFontType0FromPdfObject(d, base)
		if err != nil {
			common.Log.Debug("ERROR: While loading cid font type0 font: %v", err)
			return nil, err
		}
		font.context = cidfont
	case "CIDFontType2":
		cidfont, err := newPdfCIDFontType2FromPdfObject(d, base)
		if err != nil {
			common.Log.Debug("ERROR: While loading cid font type2 font. font=%s err=%v", base, err)
			return nil, err
		}
		font.context = cidfont
	default:
		common.Log.Debug("ERROR: Unsupported font type: font=%s", base)
		return nil, fmt.Errorf("unsupported font type: font=%s", base)
	}

	return font, nil
}

// CharcodeBytesToUnicode converts PDF character codes `data` to a Go unicode string.
//
// 9.10 Extraction of Text Content (page 292)
// The process of finding glyph descriptions in OpenType fonts by a conforming reader shall be the following:
// • For Type 1 fonts using “CFF” tables, the process shall be as described in 9.6.6.2, "Encodings
//   for Type 1 Fonts".
// • For TrueType fonts using “glyf” tables, the process shall be as described in 9.6.6.4,
//   "Encodings for TrueType Fonts". Since this process sometimes produces ambiguous results,
//   conforming writers, instead of using a simple font, shall use a Type 0 font with an Identity-H
//   encoding and use the glyph indices as character codes, as described following Table 118.
func (font PdfFont) CharcodeBytesToUnicode(data []byte) (string, int, int) {
	common.Log.Trace("showText: data=[% 02x]=%#q", data, data)

	charcodes := make([]textencoding.CharCode, 0, len(data)+len(data)%2)
	if font.baseFields().isCIDFont() {
		if len(data) == 1 {
			data = []byte{0, data[0]}
		}
		if len(data)%2 != 0 {
			common.Log.Debug("ERROR: Padding data=%+v to even length", data)
			data = append(data, 0)
		}
		for i := 0; i < len(data); i += 2 {
			b := uint16(data[i])<<8 | uint16(data[i+1])
			charcodes = append(charcodes, textencoding.CharCode(b))
		}
	} else {
		for _, b := range data {
			charcodes = append(charcodes, textencoding.CharCode(b))
		}
	}

	charstrings := make([]string, 0, len(charcodes))
	numMisses := 0
	for _, code := range charcodes {
		if font.baseFields().toUnicodeCmap != nil {
			r, ok := font.baseFields().toUnicodeCmap.CharcodeToUnicode(cmap.CharCode(code))
			if ok {
				charstrings = append(charstrings, string(r))
				continue
			}
		}
		// Fall back to encoding
		if encoder := font.Encoder(); encoder != nil {
			r, ok := encoder.CharcodeToRune(code)
			if ok {
				charstrings = append(charstrings, textencoding.RuneToString(r))
				continue
			}

			common.Log.Debug("ERROR: No rune. code=0x%04x data=[% 02x]=%#q charcodes=[% 04x] CID=%t\n"+
				"\tfont=%s\n\tencoding=%s",
				code, data, data, charcodes, font.baseFields().isCIDFont(), font, encoder)
			numMisses++
			charstrings = append(charstrings, string(cmap.MissingCodeRune))
		}
	}

	if numMisses != 0 {
		common.Log.Debug("ERROR: Couldn't convert to unicode. Using input. data=%#q=[% 02x]\n"+
			"\tnumChars=%d numMisses=%d\n"+
			"\tfont=%s",
			string(data), data, len(charcodes), numMisses, font)
	}

	out := strings.Join(charstrings, "")
	return out, len([]rune(out)), numMisses
}

// ToPdfObject converts the PdfFont object to its PDF representation.
func (font PdfFont) ToPdfObject() core.PdfObject {
	if t := font.actualFont(); t != nil {
		return t.ToPdfObject()
	}
	common.Log.Debug("ERROR: ToPdfObject Not implemented for font type=%#T. Returning null object",
		font.context)
	return core.MakeNull()
}

// Encoder returns the font's text encoder.
func (font PdfFont) Encoder() textencoding.TextEncoder {
	t := font.actualFont()
	if t == nil {
		common.Log.Debug("ERROR: Encoder not implemented for font type=%#T", font.context)
		// TODO: Should we return a default encoding?
		return nil
	}
	return t.Encoder()
}

// GetGlyphCharMetrics returns the specified char metrics for a specified glyph name.
func (font PdfFont) GetGlyphCharMetrics(glyph textencoding.GlyphName) (fonts.CharMetrics, bool) {
	t := font.actualFont()
	if t == nil {
		common.Log.Debug("ERROR: GetGlyphCharMetrics Not implemented for font type=%#T", font.context)
		return fonts.CharMetrics{}, false
	}
	return t.GetGlyphCharMetrics(glyph)
}

// actualFont returns the Font in font.context
func (font PdfFont) actualFont() pdfFont {
	if font.context == nil {
		common.Log.Debug("ERROR: actualFont. context is nil. font=%s", font)
	}
	return font.context
}

// baseFields returns the fields of `font`.context that are common to all PDF fonts.
func (font PdfFont) baseFields() *fontCommon {
	if font.context == nil {
		common.Log.Debug("ERROR: baseFields. context is nil.")
		return nil
	}
	return font.context.baseFields()
}

// fontCommon represents the fields that are common to all PDF fonts.
type fontCommon struct {
	// All fonts have these fields.
	basefont string // The font's "BaseFont" field.
	subtype  string // The font's "Subtype" field.

	// These are optional fields in the PDF font.
	toUnicode core.PdfObject // The stream containing toUnicodeCmap. We keep it around for ToPdfObject.

	// These objects are computed from optional fields in the PDF font.
	toUnicodeCmap  *cmap.CMap         // Computed from "ToUnicode".
	fontDescriptor *PdfFontDescriptor // Computed from "FontDescriptor".

	// objectNumber helps us find the font in the PDF being processed. This helps with debugging.
	objectNumber int64
}

// asPdfObjectDictionary returns `base` as a core.PdfObjectDictionary.
// It is for use in font ToPdfObject functions.
// NOTE: The returned dict's "Subtype" field is set to `subtype` if `base` doesn't have a subtype.
func (base fontCommon) asPdfObjectDictionary(subtype string) *core.PdfObjectDictionary {

	if subtype != "" && base.subtype != "" && subtype != base.subtype {
		common.Log.Debug("ERROR: asPdfObjectDictionary. Overriding subtype to %#q %s", subtype, base)
	} else if subtype == "" && base.subtype == "" {
		common.Log.Debug("ERROR: asPdfObjectDictionary no subtype. font=%s", base)
	} else if base.subtype == "" {
		base.subtype = subtype
	}

	d := core.MakeDict()
	d.Set("Type", core.MakeName("Font"))
	d.Set("BaseFont", core.MakeName(base.basefont))
	d.Set("Subtype", core.MakeName(base.subtype))

	if base.fontDescriptor != nil {
		d.Set("FontDescriptor", base.fontDescriptor.ToPdfObject())
	}
	if base.toUnicode != nil {
		d.Set("ToUnicode", base.toUnicode)
	} else if base.toUnicodeCmap != nil {
		data := base.toUnicodeCmap.Bytes()
		o, err := core.MakeStream(data, nil)
		if err != nil {
			common.Log.Debug("MakeStream failed. err=%v", err)
		} else {
			d.Set("ToUnicode", o)
		}
	}
	return d
}

// String returns a string that describes `base`.
func (base fontCommon) String() string {
	return fmt.Sprintf("FONT{%s}", base.coreString())
}

// coreString returns the contents of fontCommon.String() without the FONT{} wrapper.
func (base fontCommon) coreString() string {
	descriptor := ""
	if base.fontDescriptor != nil {
		descriptor = base.fontDescriptor.String()
	}
	return fmt.Sprintf("%#q %#q obj=%d ToUnicode=%t %s",
		base.subtype, base.basefont, base.objectNumber, base.toUnicode != nil, descriptor)
}

// isCIDFont returns true if `base` is a CID font.
func (base fontCommon) isCIDFont() bool {
	if base.subtype == "" {
		common.Log.Debug("ERROR: isCIDFont. context is nil. font=%s", base)
	}
	isCID := false
	switch base.subtype {
	case "Type0", "CIDFontType0", "CIDFontType2":
		isCID = true
	}
	common.Log.Trace("isCIDFont: isCID=%t font=%s", isCID, base)
	return isCID
}

// newFontBaseFieldsFromPdfObject returns `fontObj` as a dictionary the common fields from that
// dictionary in the fontCommon return.  If there is a problem an error is returned.
// The fontCommon is the group of fields common to all PDF fonts.
func newFontBaseFieldsFromPdfObject(fontObj core.PdfObject) (*core.PdfObjectDictionary, *fontCommon,
	error) {
	font := &fontCommon{}

	if obj, ok := fontObj.(*core.PdfIndirectObject); ok {
		font.objectNumber = obj.ObjectNumber
	}

	d, ok := core.GetDict(fontObj)
	if !ok {
		common.Log.Debug("ERROR: Font not given by a dictionary (%T)", fontObj)
		return nil, nil, ErrFontNotSupported
	}

	objtype, ok := core.GetNameVal(d.Get("Type"))
	if !ok {
		common.Log.Debug("ERROR: Font Incompatibility. Type (Required) missing")
		return nil, nil, ErrRequiredAttributeMissing
	}
	if objtype != "Font" {
		common.Log.Debug("ERROR: Font Incompatibility. Type=%q. Should be %q.", objtype, "Font")
		return nil, nil, core.ErrTypeError
	}

	subtype, ok := core.GetNameVal(d.Get("Subtype"))
	if !ok {
		common.Log.Debug("ERROR: Font Incompatibility. Subtype (Required) missing")
		return nil, nil, ErrRequiredAttributeMissing
	}
	font.subtype = subtype

	if subtype == "Type3" {
		common.Log.Debug("ERROR: Type 3 font not supprted. d=%s", d)
		return nil, nil, ErrFontNotSupported
	}

	basefont, ok := core.GetNameVal(d.Get("BaseFont"))
	if !ok {
		common.Log.Debug("ERROR: Font Incompatibility. BaseFont (Required) missing")
		return nil, nil, ErrRequiredAttributeMissing
	}
	font.basefont = basefont

	obj := d.Get("FontDescriptor")
	if obj != nil {
		fontDescriptor, err := newPdfFontDescriptorFromPdfObject(obj)
		if err != nil {
			common.Log.Debug("ERROR: Bad font descriptor. err=%v", err)
			return nil, nil, err
		}
		font.fontDescriptor = fontDescriptor
	}

	toUnicode := d.Get("ToUnicode")
	if toUnicode != nil {
		font.toUnicode = core.TraceToDirectObject(toUnicode)
		codemap, err := toUnicodeToCmap(font.toUnicode, font)
		if err != nil {
			return nil, nil, err
		}
		font.toUnicodeCmap = codemap
	}

	return d, font, nil
}

// toUnicodeToCmap returns a CMap of `toUnicode` if it exists.
func toUnicodeToCmap(toUnicode core.PdfObject, font *fontCommon) (*cmap.CMap, error) {
	toUnicodeStream, ok := core.GetStream(toUnicode)
	if !ok {
		common.Log.Debug("ERROR: toUnicodeToCmap: Not a stream (%T)", toUnicode)
		return nil, core.ErrTypeError
	}
	data, err := core.DecodeStream(toUnicodeStream)
	if err != nil {
		return nil, err
	}

	cm, err := cmap.LoadCmapFromData(data, !font.isCIDFont())
	if err != nil {
		// Show the object number of the bad cmap to help with debugging.
		common.Log.Debug("ERROR: ObjectNumber=%d err=%v", toUnicodeStream.ObjectNumber, err)
	}
	return cm, err
}

// 9.8.2 Font Descriptor Flags (page 283)
const (
	fontFlagFixedPitch  = 0x00001
	fontFlagSerif       = 0x00002
	fontFlagSymbolic    = 0x00004
	fontFlagScript      = 0x00008
	fontFlagNonsymbolic = 0x00020
	fontFlagItalic      = 0x00040
	fontFlagAllCap      = 0x10000
	fontFlagSmallCap    = 0x20000
	fontFlagForceBold   = 0x40000
)

// PdfFontDescriptor specifies metrics and other attributes of a font and can refer to a FontFile
// for embedded fonts.
// 9.8 Font Descriptors (page 281)
type PdfFontDescriptor struct {
	FontName     core.PdfObject
	FontFamily   core.PdfObject
	FontStretch  core.PdfObject
	FontWeight   core.PdfObject
	Flags        core.PdfObject
	FontBBox     core.PdfObject
	ItalicAngle  core.PdfObject
	Ascent       core.PdfObject
	Descent      core.PdfObject
	Leading      core.PdfObject
	CapHeight    core.PdfObject
	XHeight      core.PdfObject
	StemV        core.PdfObject
	StemH        core.PdfObject
	AvgWidth     core.PdfObject
	MaxWidth     core.PdfObject
	MissingWidth core.PdfObject
	FontFile     core.PdfObject // PFB
	FontFile2    core.PdfObject // TTF
	FontFile3    core.PdfObject // OTF / CFF
	CharSet      core.PdfObject

	*fontFile
	fontFile2 *fonts.TtfType

	// Additional entries for CIDFonts
	Style  core.PdfObject
	Lang   core.PdfObject
	FD     core.PdfObject
	CIDSet core.PdfObject

	// Container.
	container *core.PdfIndirectObject
}

// GetDescent returns the Descent of the font `descriptor`.
func (desc *PdfFontDescriptor) GetDescent() (float64, error) {
	return core.GetNumberAsFloat(desc.Descent)
}

// GetAscent returns the Ascent of the font `descriptor`.
func (desc *PdfFontDescriptor) GetAscent() (float64, error) {
	return core.GetNumberAsFloat(desc.Ascent)
}

// GetCapHeight returns the CapHeight of the font `descriptor`.
func (desc *PdfFontDescriptor) GetCapHeight() (float64, error) {
	return core.GetNumberAsFloat(desc.CapHeight)
}

// String returns a string describing the font descriptor.
func (desc *PdfFontDescriptor) String() string {
	var parts []string
	if desc.FontName != nil {
		parts = append(parts, desc.FontName.String())
	}
	if desc.FontFamily != nil {
		parts = append(parts, desc.FontFamily.String())
	}
	if desc.fontFile != nil {
		parts = append(parts, desc.fontFile.String())
	}
	if desc.fontFile2 != nil {
		parts = append(parts, desc.fontFile2.String())
	}
	parts = append(parts, fmt.Sprintf("FontFile3=%t", desc.FontFile3 != nil))

	return fmt.Sprintf("FONT_DESCRIPTOR{%s}", strings.Join(parts, ", "))
}

// newPdfFontDescriptorFromPdfObject loads the font descriptor from a core.PdfObject.  Can either be a
// *PdfIndirectObject or a *core.PdfObjectDictionary.
func newPdfFontDescriptorFromPdfObject(obj core.PdfObject) (*PdfFontDescriptor, error) {
	descriptor := &PdfFontDescriptor{}

	if ind, is := obj.(*core.PdfIndirectObject); is {
		descriptor.container = ind
		obj = ind.PdfObject
	}

	d, ok := obj.(*core.PdfObjectDictionary)
	if !ok {
		common.Log.Debug("ERROR: FontDescriptor not given by a dictionary (%T)", obj)
		return nil, core.ErrTypeError
	}

	if obj := d.Get("FontName"); obj != nil {
		descriptor.FontName = obj
	} else {
		common.Log.Debug("Incompatibility: FontName (Required) missing")
	}
	fontname, _ := core.GetName(descriptor.FontName)

	if obj := d.Get("Type"); obj != nil {
		oname, is := obj.(*core.PdfObjectName)
		if !is || string(*oname) != "FontDescriptor" {
			common.Log.Debug("Incompatibility: Font descriptor Type invalid (%T) font=%q %T",
				obj, fontname, descriptor.FontName)
		}
	} else {
		common.Log.Trace("Incompatibility: Type (Required) missing. font=%q %T",
			fontname, descriptor.FontName)
	}

	descriptor.FontFamily = d.Get("FontFamily")
	descriptor.FontStretch = d.Get("FontStretch")
	descriptor.FontWeight = d.Get("FontWeight")
	descriptor.Flags = d.Get("Flags")
	descriptor.FontBBox = d.Get("FontBBox")
	descriptor.ItalicAngle = d.Get("ItalicAngle")
	descriptor.Ascent = d.Get("Ascent")
	descriptor.Descent = d.Get("Descent")
	descriptor.Leading = d.Get("Leading")
	descriptor.CapHeight = d.Get("CapHeight")
	descriptor.XHeight = d.Get("XHeight")
	descriptor.StemV = d.Get("StemV")
	descriptor.StemH = d.Get("StemH")
	descriptor.AvgWidth = d.Get("AvgWidth")
	descriptor.MaxWidth = d.Get("MaxWidth")
	descriptor.MissingWidth = d.Get("MissingWidth")
	descriptor.FontFile = d.Get("FontFile")
	descriptor.FontFile2 = d.Get("FontFile2")
	descriptor.FontFile3 = d.Get("FontFile3")
	descriptor.CharSet = d.Get("CharSet")
	descriptor.Style = d.Get("Style")
	descriptor.Lang = d.Get("Lang")
	descriptor.FD = d.Get("FD")
	descriptor.CIDSet = d.Get("CIDSet")

	if descriptor.FontFile != nil {
		fontFile, err := newFontFileFromPdfObject(descriptor.FontFile)
		if err != nil {
			return descriptor, err
		}
		common.Log.Trace("fontFile=%s", fontFile)
		descriptor.fontFile = fontFile
	}
	if descriptor.FontFile2 != nil {
		fontFile2, err := fonts.NewFontFile2FromPdfObject(descriptor.FontFile2)
		if err != nil {
			return descriptor, err
		}
		common.Log.Trace("fontFile2=%s", fontFile2.String())
		descriptor.fontFile2 = &fontFile2
	}
	return descriptor, nil
}

// ToPdfObject returns the PdfFontDescriptor as a PDF dictionary inside an indirect object.
func (desc *PdfFontDescriptor) ToPdfObject() core.PdfObject {
	d := core.MakeDict()
	if desc.container == nil {
		desc.container = &core.PdfIndirectObject{}
	}
	desc.container.PdfObject = d

	d.Set("Type", core.MakeName("FontDescriptor"))

	if desc.FontName != nil {
		d.Set("FontName", desc.FontName)
	}

	if desc.FontFamily != nil {
		d.Set("FontFamily", desc.FontFamily)
	}

	if desc.FontStretch != nil {
		d.Set("FontStretch", desc.FontStretch)
	}

	if desc.FontWeight != nil {
		d.Set("FontWeight", desc.FontWeight)
	}

	if desc.Flags != nil {
		d.Set("Flags", desc.Flags)
	}

	if desc.FontBBox != nil {
		d.Set("FontBBox", desc.FontBBox)
	}

	if desc.ItalicAngle != nil {
		d.Set("ItalicAngle", desc.ItalicAngle)
	}

	if desc.Ascent != nil {
		d.Set("Ascent", desc.Ascent)
	}

	if desc.Descent != nil {
		d.Set("Descent", desc.Descent)
	}

	if desc.Leading != nil {
		d.Set("Leading", desc.Leading)
	}

	if desc.CapHeight != nil {
		d.Set("CapHeight", desc.CapHeight)
	}

	if desc.XHeight != nil {
		d.Set("XHeight", desc.XHeight)
	}

	if desc.StemV != nil {
		d.Set("StemV", desc.StemV)
	}

	if desc.StemH != nil {
		d.Set("StemH", desc.StemH)
	}

	if desc.AvgWidth != nil {
		d.Set("AvgWidth", desc.AvgWidth)
	}

	if desc.MaxWidth != nil {
		d.Set("MaxWidth", desc.MaxWidth)
	}

	if desc.MissingWidth != nil {
		d.Set("MissingWidth", desc.MissingWidth)
	}

	if desc.FontFile != nil {
		d.Set("FontFile", desc.FontFile)
	}

	if desc.FontFile2 != nil {
		d.Set("FontFile2", desc.FontFile2)
	}

	if desc.FontFile3 != nil {
		d.Set("FontFile3", desc.FontFile3)
	}

	if desc.CharSet != nil {
		d.Set("CharSet", desc.CharSet)
	}

	if desc.Style != nil {
		d.Set("FontName", desc.FontName)
	}

	if desc.Lang != nil {
		d.Set("Lang", desc.Lang)
	}

	if desc.FD != nil {
		d.Set("FD", desc.FD)
	}

	if desc.CIDSet != nil {
		d.Set("CIDSet", desc.CIDSet)
	}

	return desc.container
}
