package app

import "testing"

func TestBuildCodexTurnInputUsesImageDataURL(t *testing.T) {
	input := buildCodexTurnInput("inspect this", []messageAttachment{{
		Type:      "image",
		LocalPath: "/tmp/pocketcode-images-123/red-dot.png",
		LocalData: "aW1hZ2U=",
		MimeType:  "image/png",
	}})

	if len(input) != 2 {
		t.Fatalf("expected text and image input blocks, got %d", len(input))
	}
	image := input[1]
	if image["type"] != "image" {
		t.Fatalf("unexpected image type: %v", image["type"])
	}
	if image["url"] != "data:image/png;base64,aW1hZ2U=" {
		t.Fatalf("unexpected image url: %v", image["url"])
	}
	if _, ok := image["data"]; ok {
		t.Fatalf("image input must not use data field")
	}
}
