// Code generated from the bench environment's Python 3.14.4 str.upper, Unicode
// 16.0.0: every code point whose full upper case is more than one character,
// SpecialCasing.txt's unconditional mappings, which Polars' to_uppercase applies and
// Go's unicode.ToUpper, mapping a rune to a rune, cannot (step 178). DO NOT EDIT.

package kernel

var upperExpansions = map[rune]string{
	0x00DF: "SS",                 // latin small letter sharp s
	0x0149: "\u02BCN",            // latin small letter n preceded by apostrophe
	0x01F0: "J\u030C",            // latin small letter j with caron
	0x0390: "\u0399\u0308\u0301", // greek small letter iota with dialytika and tonos
	0x03B0: "\u03A5\u0308\u0301", // greek small letter upsilon with dialytika and tonos
	0x0587: "\u0535\u0552",       // armenian small ligature ech yiwn
	0x1E96: "H\u0331",            // latin small letter h with line below
	0x1E97: "T\u0308",            // latin small letter t with diaeresis
	0x1E98: "W\u030A",            // latin small letter w with ring above
	0x1E99: "Y\u030A",            // latin small letter y with ring above
	0x1E9A: "A\u02BE",            // latin small letter a with right half ring
	0x1F50: "\u03A5\u0313",       // greek small letter upsilon with psili
	0x1F52: "\u03A5\u0313\u0300", // greek small letter upsilon with psili and varia
	0x1F54: "\u03A5\u0313\u0301", // greek small letter upsilon with psili and oxia
	0x1F56: "\u03A5\u0313\u0342", // greek small letter upsilon with psili and perispomeni
	0x1F80: "\u1F08\u0399",       // greek small letter alpha with psili and ypogegrammeni
	0x1F81: "\u1F09\u0399",       // greek small letter alpha with dasia and ypogegrammeni
	0x1F82: "\u1F0A\u0399",       // greek small letter alpha with psili and varia and ypogegrammeni
	0x1F83: "\u1F0B\u0399",       // greek small letter alpha with dasia and varia and ypogegrammeni
	0x1F84: "\u1F0C\u0399",       // greek small letter alpha with psili and oxia and ypogegrammeni
	0x1F85: "\u1F0D\u0399",       // greek small letter alpha with dasia and oxia and ypogegrammeni
	0x1F86: "\u1F0E\u0399",       // greek small letter alpha with psili and perispomeni and ypogegrammeni
	0x1F87: "\u1F0F\u0399",       // greek small letter alpha with dasia and perispomeni and ypogegrammeni
	0x1F88: "\u1F08\u0399",       // greek capital letter alpha with psili and prosgegrammeni
	0x1F89: "\u1F09\u0399",       // greek capital letter alpha with dasia and prosgegrammeni
	0x1F8A: "\u1F0A\u0399",       // greek capital letter alpha with psili and varia and prosgegrammeni
	0x1F8B: "\u1F0B\u0399",       // greek capital letter alpha with dasia and varia and prosgegrammeni
	0x1F8C: "\u1F0C\u0399",       // greek capital letter alpha with psili and oxia and prosgegrammeni
	0x1F8D: "\u1F0D\u0399",       // greek capital letter alpha with dasia and oxia and prosgegrammeni
	0x1F8E: "\u1F0E\u0399",       // greek capital letter alpha with psili and perispomeni and prosgegrammeni
	0x1F8F: "\u1F0F\u0399",       // greek capital letter alpha with dasia and perispomeni and prosgegrammeni
	0x1F90: "\u1F28\u0399",       // greek small letter eta with psili and ypogegrammeni
	0x1F91: "\u1F29\u0399",       // greek small letter eta with dasia and ypogegrammeni
	0x1F92: "\u1F2A\u0399",       // greek small letter eta with psili and varia and ypogegrammeni
	0x1F93: "\u1F2B\u0399",       // greek small letter eta with dasia and varia and ypogegrammeni
	0x1F94: "\u1F2C\u0399",       // greek small letter eta with psili and oxia and ypogegrammeni
	0x1F95: "\u1F2D\u0399",       // greek small letter eta with dasia and oxia and ypogegrammeni
	0x1F96: "\u1F2E\u0399",       // greek small letter eta with psili and perispomeni and ypogegrammeni
	0x1F97: "\u1F2F\u0399",       // greek small letter eta with dasia and perispomeni and ypogegrammeni
	0x1F98: "\u1F28\u0399",       // greek capital letter eta with psili and prosgegrammeni
	0x1F99: "\u1F29\u0399",       // greek capital letter eta with dasia and prosgegrammeni
	0x1F9A: "\u1F2A\u0399",       // greek capital letter eta with psili and varia and prosgegrammeni
	0x1F9B: "\u1F2B\u0399",       // greek capital letter eta with dasia and varia and prosgegrammeni
	0x1F9C: "\u1F2C\u0399",       // greek capital letter eta with psili and oxia and prosgegrammeni
	0x1F9D: "\u1F2D\u0399",       // greek capital letter eta with dasia and oxia and prosgegrammeni
	0x1F9E: "\u1F2E\u0399",       // greek capital letter eta with psili and perispomeni and prosgegrammeni
	0x1F9F: "\u1F2F\u0399",       // greek capital letter eta with dasia and perispomeni and prosgegrammeni
	0x1FA0: "\u1F68\u0399",       // greek small letter omega with psili and ypogegrammeni
	0x1FA1: "\u1F69\u0399",       // greek small letter omega with dasia and ypogegrammeni
	0x1FA2: "\u1F6A\u0399",       // greek small letter omega with psili and varia and ypogegrammeni
	0x1FA3: "\u1F6B\u0399",       // greek small letter omega with dasia and varia and ypogegrammeni
	0x1FA4: "\u1F6C\u0399",       // greek small letter omega with psili and oxia and ypogegrammeni
	0x1FA5: "\u1F6D\u0399",       // greek small letter omega with dasia and oxia and ypogegrammeni
	0x1FA6: "\u1F6E\u0399",       // greek small letter omega with psili and perispomeni and ypogegrammeni
	0x1FA7: "\u1F6F\u0399",       // greek small letter omega with dasia and perispomeni and ypogegrammeni
	0x1FA8: "\u1F68\u0399",       // greek capital letter omega with psili and prosgegrammeni
	0x1FA9: "\u1F69\u0399",       // greek capital letter omega with dasia and prosgegrammeni
	0x1FAA: "\u1F6A\u0399",       // greek capital letter omega with psili and varia and prosgegrammeni
	0x1FAB: "\u1F6B\u0399",       // greek capital letter omega with dasia and varia and prosgegrammeni
	0x1FAC: "\u1F6C\u0399",       // greek capital letter omega with psili and oxia and prosgegrammeni
	0x1FAD: "\u1F6D\u0399",       // greek capital letter omega with dasia and oxia and prosgegrammeni
	0x1FAE: "\u1F6E\u0399",       // greek capital letter omega with psili and perispomeni and prosgegrammeni
	0x1FAF: "\u1F6F\u0399",       // greek capital letter omega with dasia and perispomeni and prosgegrammeni
	0x1FB2: "\u1FBA\u0399",       // greek small letter alpha with varia and ypogegrammeni
	0x1FB3: "\u0391\u0399",       // greek small letter alpha with ypogegrammeni
	0x1FB4: "\u0386\u0399",       // greek small letter alpha with oxia and ypogegrammeni
	0x1FB6: "\u0391\u0342",       // greek small letter alpha with perispomeni
	0x1FB7: "\u0391\u0342\u0399", // greek small letter alpha with perispomeni and ypogegrammeni
	0x1FBC: "\u0391\u0399",       // greek capital letter alpha with prosgegrammeni
	0x1FC2: "\u1FCA\u0399",       // greek small letter eta with varia and ypogegrammeni
	0x1FC3: "\u0397\u0399",       // greek small letter eta with ypogegrammeni
	0x1FC4: "\u0389\u0399",       // greek small letter eta with oxia and ypogegrammeni
	0x1FC6: "\u0397\u0342",       // greek small letter eta with perispomeni
	0x1FC7: "\u0397\u0342\u0399", // greek small letter eta with perispomeni and ypogegrammeni
	0x1FCC: "\u0397\u0399",       // greek capital letter eta with prosgegrammeni
	0x1FD2: "\u0399\u0308\u0300", // greek small letter iota with dialytika and varia
	0x1FD3: "\u0399\u0308\u0301", // greek small letter iota with dialytika and oxia
	0x1FD6: "\u0399\u0342",       // greek small letter iota with perispomeni
	0x1FD7: "\u0399\u0308\u0342", // greek small letter iota with dialytika and perispomeni
	0x1FE2: "\u03A5\u0308\u0300", // greek small letter upsilon with dialytika and varia
	0x1FE3: "\u03A5\u0308\u0301", // greek small letter upsilon with dialytika and oxia
	0x1FE4: "\u03A1\u0313",       // greek small letter rho with psili
	0x1FE6: "\u03A5\u0342",       // greek small letter upsilon with perispomeni
	0x1FE7: "\u03A5\u0308\u0342", // greek small letter upsilon with dialytika and perispomeni
	0x1FF2: "\u1FFA\u0399",       // greek small letter omega with varia and ypogegrammeni
	0x1FF3: "\u03A9\u0399",       // greek small letter omega with ypogegrammeni
	0x1FF4: "\u038F\u0399",       // greek small letter omega with oxia and ypogegrammeni
	0x1FF6: "\u03A9\u0342",       // greek small letter omega with perispomeni
	0x1FF7: "\u03A9\u0342\u0399", // greek small letter omega with perispomeni and ypogegrammeni
	0x1FFC: "\u03A9\u0399",       // greek capital letter omega with prosgegrammeni
	0xFB00: "FF",                 // latin small ligature ff
	0xFB01: "FI",                 // latin small ligature fi
	0xFB02: "FL",                 // latin small ligature fl
	0xFB03: "FFI",                // latin small ligature ffi
	0xFB04: "FFL",                // latin small ligature ffl
	0xFB05: "ST",                 // latin small ligature long s t
	0xFB06: "ST",                 // latin small ligature st
	0xFB13: "\u0544\u0546",       // armenian small ligature men now
	0xFB14: "\u0544\u0535",       // armenian small ligature men ech
	0xFB15: "\u0544\u053B",       // armenian small ligature men ini
	0xFB16: "\u054E\u0546",       // armenian small ligature vew now
	0xFB17: "\u0544\u053D",       // armenian small ligature men xeh
}
