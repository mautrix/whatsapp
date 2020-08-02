// +build !cgo

package main

import (
	"image"
	"io"

	"golang.org/x/image/webp"
)

func decodeWebp(r io.Reader) (image.Image, error) {
	return webp.Decode(r)
}
