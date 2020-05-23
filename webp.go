// +build cgo

package main

import (
	"image"
	"io"

	"github.com/chai2010/webp"
)

func decodeWebp(r io.Reader) (image.Image, error) {
	return webp.Decode(r)
}
