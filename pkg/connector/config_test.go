package connector

import (
	"os"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestExampleConfigDoesNotAdvertiseUnsupportedVideoModes(t *testing.T) {
	data, err := os.ReadFile("example-config.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var config map[string]any
	if err = yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	voip, ok := config["voip"].(map[string]any)
	if !ok {
		t.Fatal("example config has no voip section")
	}
	video, ok := voip["video"].(map[string]any)
	if !ok {
		t.Fatal("example config has no voip.video section")
	}
	for _, unsupported := range []string{"require_h264", "allow_transcode"} {
		if _, exists := video[unsupported]; exists {
			t.Errorf("example config advertises unsupported voip.video.%s option", unsupported)
		}
	}
}
