package desktop

import (
	"bytes"
	"encoding/binary"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
)

// The files the launchers are made of, in every build so their tests run on every system.

// bundleExe is the executable's name inside Murmur.app.
const bundleExe = brand.Product

// icoFrom makes an .ico holding the PNGs as they are, which Windows has read since Vista.
func icoFrom(pngs ...[]byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, uint16(len(pngs))})
	offset := 6 + 16*len(pngs)
	for _, p := range pngs {
		w, h := pngSize(p)
		entry := struct {
			W, H, Colors, Reserved uint8
			Planes, Bits           uint16
			Size, Offset           uint32
		}{uint8(w % 256), uint8(h % 256), 0, 0, 1, 32, uint32(len(p)), uint32(offset)}
		binary.Write(&b, binary.LittleEndian, entry)
		offset += len(p)
	}
	for _, p := range pngs {
		b.Write(p)
	}
	return b.Bytes()
}

// pngSize reads a PNG's width and height from its header.
func pngSize(p []byte) (w, h int) {
	if len(p) < 24 {
		return 0, 0
	}
	return int(binary.BigEndian.Uint32(p[16:20])), int(binary.BigEndian.Uint32(p[20:24]))
}

func infoPlist() []byte {
	kv := [][2]string{
		{"CFBundleExecutable", bundleExe},
		{"CFBundleIdentifier", brand.AppID},
		{"CFBundleName", brand.Product},
		{"CFBundleDisplayName", brand.Product},
		{"CFBundleIconFile", bundleExe},
		{"CFBundlePackageType", "APPL"},
		{"CFBundleInfoDictionaryVersion", "6.0"},
		{"LSMinimumSystemVersion", "11.0"},
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	for _, p := range kv {
		b.WriteString("\t<key>" + p[0] + "</key>\n\t<string>" + p[1] + "</string>\n")
	}
	b.WriteString("\t<key>NSHighResolutionCapable</key>\n\t<true/>\n</dict>\n</plist>\n")
	return []byte(b.String())
}

// icnsFrom makes an .icns holding one 512 px PNG (type ic09), which macOS scales for the rest.
func icnsFrom(png []byte) []byte {
	var b bytes.Buffer
	b.WriteString("icns")
	binary.Write(&b, binary.BigEndian, uint32(8+8+len(png)))
	b.WriteString("ic09")
	binary.Write(&b, binary.BigEndian, uint32(8+len(png)))
	b.Write(png)
	return b.Bytes()
}

func desktopEntry(exe string) string {
	return "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=" + brand.Product + "\n" +
		"Comment=Messages between the coding agents in your repos\n" +
		"Exec=" + execQuote(exe) + " app --from-launcher\n" +
		"Icon=" + brand.AppID + "\n" +
		"Terminal=false\n" +
		"Categories=Development;Network;\n"
}

// execQuote quotes an argument for a .desktop file's Exec key: in double quotes, with ", `, $ and
// \ escaped, the backslashes doubled again, as the key's value is itself unescaped once, and a % as
// %%, which would otherwise start a field code such as %f.
func execQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '`', '$':
			b.WriteString(`\\`)
		case '\\':
			b.WriteString(`\\\`)
		case '%':
			b.WriteByte('%')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
