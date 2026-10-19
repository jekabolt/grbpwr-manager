package bucket

import (
	"encoding/binary"
	"image"
	"image/draw"
)

// jpegEXIFOrientation returns the EXIF Orientation tag (1..8) of a JPEG, or 1 when
// absent or unreadable. Phones (iPhone above all) store the sensor raster as shot and
// put the rotation into this tag; image/jpeg ignores it, and the WebP re-encode drops
// the EXIF block, so without applying it the stored picture lies on its side.
func jpegEXIFOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			i += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // start of scan / end: no EXIF before the raster
			return 1
		}
		segLen := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+segLen]
		if marker == 0xE1 && len(seg) >= 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + segLen
	}
	return 1
}

// tiffOrientation reads tag 0x0112 from IFD0 of a TIFF header.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	ifd := int(bo.Uint32(t[4:8]))
	if ifd < 8 || ifd+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[ifd : ifd+2]))
	for k := 0; k < n; k++ {
		e := ifd + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:e+2]) == 0x0112 {
			v := int(bo.Uint16(t[e+8 : e+10]))
			if v < 1 || v > 8 {
				return 1
			}
			return v
		}
	}
	return 1
}

// applyOrientation returns img transformed so it displays upright for the given EXIF
// orientation (1 = as is). Orientations 5..8 swap width and height.
func applyOrientation(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	src := image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(src, src.Bounds(), img, img.Bounds().Min, draw.Src)
	w, h := src.Bounds().Dx(), src.Bounds().Dy()

	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2: // mirror horizontal
				dx, dy = w-1-x, y
			case 3: // rotate 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirror vertical
				dx, dy = x, h-1-y
			case 5: // transpose
				dx, dy = y, x
			case 6: // rotate 90 CW
				dx, dy = h-1-y, x
			case 7: // transverse
				dx, dy = h-1-y, w-1-x
			case 8: // rotate 90 CCW
				dx, dy = y, w-1-x
			}
			so := src.PixOffset(x, y)
			do := dst.PixOffset(dx, dy)
			copy(dst.Pix[do:do+4], src.Pix[so:so+4])
		}
	}
	return dst
}
