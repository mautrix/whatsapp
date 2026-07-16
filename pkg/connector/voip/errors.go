package voip

import "errors"

var ErrNotEnabled = errors.New("voip: MatrixRTC LiveKit bridge is not enabled")
var ErrCallNotFound = errors.New("voip: call not found")
