package main

import (
	"bytes"
	"encoding/binary"
	"sync"
)

type iconPixel struct {
	r uint8
	g uint8
	b uint8
	a uint8
}

var (
	trayIconCache     [4][101][]byte
	trayIconCacheOnce sync.Once
)

func buildTrayIcon(percent int, charging bool, alert bool) []byte {
	trayIconCacheOnce.Do(precomputeTrayIcons)
	percent = clampPercent(percent)
	return trayIconCache[iconVariantIndex(charging, alert)][percent]
}

func precomputeTrayIcons() {
	for percent := 0; percent <= 100; percent++ {
		trayIconCache[iconVariantIndex(false, false)][percent] = buildTrayIconBytes(percent, false, false)
		trayIconCache[iconVariantIndex(true, false)][percent] = buildTrayIconBytes(percent, true, false)
		trayIconCache[iconVariantIndex(false, true)][percent] = buildTrayIconBytes(percent, false, true)
		trayIconCache[iconVariantIndex(true, true)][percent] = buildTrayIconBytes(percent, true, true)
	}
}

func iconVariantIndex(charging bool, alert bool) int {
	index := 0
	if charging {
		index |= 1
	}
	if alert {
		index |= 2
	}
	return index
}

func buildTrayIconBytes(percent int, charging bool, alert bool) []byte {
	percent = clampPercent(percent)
	pixels := make([]iconPixel, 16*16)

	outline := iconPixel{r: 44, g: 52, b: 64, a: 255}
	background := iconPixel{r: 235, g: 239, b: 244, a: 255}
	fill := batteryFillColor(percent, alert)

	drawRect(pixels, 1, 4, 13, 12, outline)
	drawRect(pixels, 13, 6, 15, 10, outline)
	drawRect(pixels, 2, 5, 12, 11, background)

	fillWidth := (percent*10 + 50) / 100
	if percent > 0 && fillWidth == 0 {
		fillWidth = 1
	}
	if fillWidth > 0 {
		drawRect(pixels, 2, 5, 2+fillWidth, 11, fill)
	}

	if charging {
		drawBolt(pixels)
	}
	if alert && !charging {
		drawAlert(pixels)
	}

	return encodeICO(pixels)
}

func batteryFillColor(percent int, alert bool) iconPixel {
	if alert {
		return iconPixel{r: 232, g: 141, b: 35, a: 255}
	}
	if percent <= 20 {
		return iconPixel{r: 219, g: 68, b: 55, a: 255}
	}
	if percent <= 50 {
		return iconPixel{r: 244, g: 180, b: 0, a: 255}
	}
	return iconPixel{r: 15, g: 157, b: 88, a: 255}
}

func drawBolt(pixels []iconPixel) {
	bolt := iconPixel{r: 66, g: 133, b: 244, a: 255}
	coordinates := [][2]int{
		{8, 4}, {7, 6}, {8, 6}, {6, 9}, {8, 9}, {7, 12}, {10, 8}, {9, 8},
	}
	for _, coordinate := range coordinates {
		setPixel(pixels, coordinate[0], coordinate[1], bolt)
		setPixel(pixels, coordinate[0]-1, coordinate[1], bolt)
	}
	setPixel(pixels, 8, 5, bolt)
	setPixel(pixels, 7, 10, bolt)
}

func drawAlert(pixels []iconPixel) {
	alert := iconPixel{r: 121, g: 85, b: 72, a: 255}
	for y := 6; y <= 9; y++ {
		setPixel(pixels, 7, y, alert)
		setPixel(pixels, 8, y, alert)
	}
	setPixel(pixels, 7, 11, alert)
	setPixel(pixels, 8, 11, alert)
}

func drawRect(pixels []iconPixel, left int, top int, right int, bottom int, color iconPixel) {
	for y := top; y < bottom; y++ {
		for x := left; x < right; x++ {
			setPixel(pixels, x, y, color)
		}
	}
}

func setPixel(pixels []iconPixel, x int, y int, color iconPixel) {
	if x < 0 || x >= 16 || y < 0 || y >= 16 {
		return
	}
	pixels[y*16+x] = color
}

func clampPercent(percent int) int {
	if percent < 0 {
		return 0
	}
	if percent > 100 {
		return 100
	}
	return percent
}

func encodeICO(pixels []iconPixel) []byte {
	const (
		iconSize  = 16
		headerLen = 40
		maskBytes = iconSize * 4
	)

	xorBytes := uint32(iconSize * iconSize * 4)
	imageBytes := uint32(headerLen) + xorBytes + uint32(maskBytes)
	imageOffset := uint32(6 + 16)

	buffer := bytes.NewBuffer(make([]byte, 0, imageOffset+imageBytes))
	binary.Write(buffer, binary.LittleEndian, uint16(0))
	binary.Write(buffer, binary.LittleEndian, uint16(1))
	binary.Write(buffer, binary.LittleEndian, uint16(1))
	buffer.WriteByte(iconSize)
	buffer.WriteByte(iconSize)
	buffer.WriteByte(0)
	buffer.WriteByte(0)
	binary.Write(buffer, binary.LittleEndian, uint16(1))
	binary.Write(buffer, binary.LittleEndian, uint16(32))
	binary.Write(buffer, binary.LittleEndian, imageBytes)
	binary.Write(buffer, binary.LittleEndian, imageOffset)

	binary.Write(buffer, binary.LittleEndian, uint32(headerLen))
	binary.Write(buffer, binary.LittleEndian, int32(iconSize))
	binary.Write(buffer, binary.LittleEndian, int32(iconSize*2))
	binary.Write(buffer, binary.LittleEndian, uint16(1))
	binary.Write(buffer, binary.LittleEndian, uint16(32))
	binary.Write(buffer, binary.LittleEndian, uint32(0))
	binary.Write(buffer, binary.LittleEndian, xorBytes)
	binary.Write(buffer, binary.LittleEndian, int32(0))
	binary.Write(buffer, binary.LittleEndian, int32(0))
	binary.Write(buffer, binary.LittleEndian, uint32(0))
	binary.Write(buffer, binary.LittleEndian, uint32(0))

	for y := iconSize - 1; y >= 0; y-- {
		for x := 0; x < iconSize; x++ {
			pixel := pixels[y*iconSize+x]
			buffer.WriteByte(pixel.b)
			buffer.WriteByte(pixel.g)
			buffer.WriteByte(pixel.r)
			buffer.WriteByte(pixel.a)
		}
	}

	buffer.Write(make([]byte, maskBytes))
	return buffer.Bytes()
}
