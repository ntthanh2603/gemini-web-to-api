package providers

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func testPNGWithSize(t *testing.T, w, h int, opaque bool) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	if opaque {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.Set(x, y, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestValidateMaskAcceptsMatchingPNG: a same-size PNG is valid whether or not
// it has transparent pixels (transparency carries the repaint region; an
// all-opaque mask simply means "repaint nothing", which is the client's call).
func TestValidateMaskAcceptsMatchingPNG(t *testing.T) {
	base := testPNGWithSize(t, 8, 6, true)
	mask := testPNGWithSize(t, 8, 6, false)
	if _, _, err := validateMaskPNG(base, mask); err != nil {
		t.Fatalf("matching mask rejected: %v", err)
	}
}

// TestValidateMaskRejectsMismatchAndGarbage pins the protocol rules that must
// be enforced before anything is uploaded.
func TestValidateMaskRejectsMismatchAndGarbage(t *testing.T) {
	base := testPNGWithSize(t, 8, 6, true)

	if _, _, err := validateMaskPNG(base, testPNGWithSize(t, 8, 7, false)); err == nil || !strings.Contains(err.Error(), "same dimensions") {
		t.Errorf("size mismatch must be rejected with a dimension error, got %v", err)
	}
	if _, _, err := validateMaskPNG(base, []byte("not a png")); err == nil || !strings.Contains(err.Error(), "not a valid PNG") {
		t.Errorf("garbage mask must be rejected as invalid PNG, got %v", err)
	}
	if _, _, err := validateMaskPNG([]byte("not an image"), testPNGWithSize(t, 8, 6, false)); err == nil {
		t.Errorf("undecodable base image must be rejected")
	}
}

// TestBuildMaskEditPromptKeepsInstructionAfterGuide pins the prompt contract:
// the guide explaining the two images comes first; the user's instruction is
// appended verbatim under an explicit label, so the model can never mistake
// the guide for the task or the task for the guide.
func TestBuildMaskEditPromptKeepsInstructionAfterGuide(t *testing.T) {
	got := buildMaskEditPrompt("Replace the sky with a starry night", "1536x1024")
	for _, want := range []string{
		"The first image is a mask for the second image",
		"Edit instruction: Replace the sky with a starry night",
		"output only the completed second image, without the mask",
		"Requested size/aspect: 1536x1024",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "The first image is a mask") > strings.Index(got, "Edit instruction:") {
		t.Errorf("guide must precede the instruction:\n%s", got)
	}
}

// TestCreateMaskedEditSendsMaskFirstThenBase drives the real request path and
// asserts the wire order the guide prefix depends on: mask first, base second.
// If this order ever flips, the prompt silently lies to the model and every
// masked edit degrades into an unmasked one.
func TestCreateMaskedEditSendsMaskFirstThenBase(t *testing.T) {
	basePNG := testPNGWithSize(t, 8, 6, true)
	maskPNG := testPNGWithSize(t, 8, 6, false)
	stub, httpClient := newWireStub(t, "https://lh3.googleusercontent.com/gg-dl/masked-out", maskPNG)

	client := &Client{at: "token", cookieHeader: "SID=x", cachedModels: wireTestModels(), httpClient: httpClient, log: zap.NewNop()}
	resp, err := client.CreateMaskedEdit(context.Background(), MaskedEditRequest{
		Prompt: "Replace the sky with a starry night",
		Model:  "gemini-pro",
		Image:  InputFile{Name: "photo.png", MimeType: "image/png", Data: basePNG},
		Mask:   InputFile{Name: "mask.png", MimeType: "image/png", Data: maskPNG},
	})
	if err != nil {
		t.Fatalf("CreateMaskedEdit: %v", err)
	}

	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.uploadedNames) != 2 || stub.uploadedNames[0] != "mask.png" || stub.uploadedNames[1] != "photo.png" {
		t.Errorf("mask must be uploaded first, base second; got %v", stub.uploadedNames)
	}
	// Uploaded bytes must be the originals: this mode does no rendering.
	if string(stub.uploadedBytes[0]) != string(maskPNG) || string(stub.uploadedBytes[1]) != string(basePNG) {
		t.Errorf("mask/base bytes must be sent unmodified")
	}
	if len(stub.attached) != 2 || !strings.HasSuffix(stub.attached[0], " mask.png") || !strings.HasSuffix(stub.attached[1], " photo.png") {
		t.Errorf("attachments out of order: %v", stub.attached)
	}
	if !strings.Contains(stub.promptSent, "Edit instruction: Replace the sky with a starry night") {
		t.Errorf("instruction lost or unlabelled: %q", stub.promptSent)
	}
	if len(resp.Data) != 1 || resp.Data[0].B64JSON == "" {
		t.Errorf("result must carry downloaded bytes: %+v", resp.Data)
	}
}

// TestCreateMaskedEditValidatesBeforeAnyRequest: a bad mask must fail before
// upload, since uploads cost time and a Gemini quota unit.
func TestCreateMaskedEditValidatesBeforeAnyRequest(t *testing.T) {
	stub, _ := newWireStub(t, "", nil)
	client := &Client{log: zap.NewNop()}
	_, err := client.CreateMaskedEdit(context.Background(), MaskedEditRequest{
		Prompt: "x",
		Image:  InputFile{Name: "a.png", Data: testPNGWithSize(t, 4, 4, true)},
		Mask:   InputFile{Name: "m.png", Data: testPNGWithSize(t, 5, 4, false)},
	})
	if err == nil || !strings.Contains(err.Error(), "same dimensions") {
		t.Errorf("size mismatch must fail with a dimension error, got %v", err)
	}
	if len(stub.seen) != 0 {
		t.Errorf("no request may be sent for an invalid mask; sent %v", stub.seen)
	}
}
