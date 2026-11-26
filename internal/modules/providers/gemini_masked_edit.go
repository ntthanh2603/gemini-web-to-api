package providers

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/jpeg" // register JPEG for base-image dimension checks
	"image/png"    // masks must be PNG; decode explicitly rather than by sniffing
	"strings"
	"time"
)

// MaskedEditRequest is an edit restricted to a region: Image is repainted only
// where Mask is transparent, as in the OpenAI Images API.
type MaskedEditRequest struct {
	Prompt string
	Model  string
	Size   string
	Image  InputFile
	Mask   InputFile
}

// maskGuidePrefix explains the two attachments to the model. Gemini Web has no
// inpainting RPC, so a masked edit is a normal generation with two reference
// images and a guide that states which is which and what "bright" means.
//
// The wording describes the mask as the model SEES it: repaint regions arrive
// fully transparent from OpenAI-compatible clients and are composited onto a
// light background on Gemini's side, so they read as bright; keep regions are
// opaque black. This mirrors the wording validated end-to-end by
// chatgpt-image-pool's MASKMODE=mask.
const maskGuidePrefix = "The first image is a mask for the second image: the bright area of the mask marks the region to repaint; " +
	"the black area means keep unchanged. Repaint exactly the marked region as instructed; keep everything else " +
	"unchanged; output only the completed second image, without the mask."

// buildMaskEditPrompt places the guide first and the user's instruction after
// an explicit label, so neither can be mistaken for the other. An explicit size
// is appended as guidance, the same way /images/generations passes it on.
func buildMaskEditPrompt(instruction, size string) string {
	b := maskGuidePrefix + "\n\nEdit instruction: " + strings.TrimSpace(instruction)
	if s := strings.TrimSpace(size); s != "" {
		b += "\nRequested size/aspect: " + s
	}
	return b
}

// ValidateMaskPNG is the exported form of validateMaskPNG for callers that
// want to reject a bad mask before handing the request to the provider.
func ValidateMaskPNG(baseImage, mask []byte) error {
	_, _, err := validateMaskPNG(baseImage, mask)
	return err
}

// validateMaskPNG decodes both images and enforces the protocol rule the guide
// relies on: the mask must be a PNG with exactly the base image's dimensions,
// otherwise "the same spot in the mask" is meaningless.
func validateMaskPNG(baseImage, mask []byte) (base image.Image, maskImg image.Image, err error) {
	base, _, err = image.Decode(bytes.NewReader(baseImage))
	if err != nil {
		return nil, nil, fmt.Errorf("image is not a decodable image")
	}
	maskImg, err = png.Decode(bytes.NewReader(mask))
	if err != nil {
		return nil, nil, fmt.Errorf("mask is not a valid PNG")
	}
	bb, mb := base.Bounds(), maskImg.Bounds()
	if bb.Dx() != mb.Dx() || bb.Dy() != mb.Dy() {
		return nil, nil, fmt.Errorf("mask must have the same dimensions as the first image (mask %dx%d, image %dx%d)",
			mb.Dx(), mb.Dy(), bb.Dx(), bb.Dy())
	}
	return base, maskImg, nil
}

// CreateMaskedEdit repaints the transparent region of Mask on Image following
// the instruction, and returns the result with downloaded bytes.
//
// It sends the mask and the base image unmodified — no overlay rendering, no
// extra RPC — relying on the guide prefix to convey their relationship.
func (c *Client) CreateMaskedEdit(ctx context.Context, req MaskedEditRequest) (*ImageEditResult, error) {
	if c.maskedEditFn != nil {
		return c.maskedEditFn(ctx, req)
	}
	instruction := strings.TrimSpace(req.Prompt)
	if instruction == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if len(req.Image.Data) == 0 {
		return nil, fmt.Errorf("image is required")
	}
	if len(req.Mask.Data) == 0 {
		return nil, fmt.Errorf("mask is required")
	}
	if _, _, err := validateMaskPNG(req.Image.Data, req.Mask.Data); err != nil {
		return nil, err
	}

	imageName := strings.TrimSpace(req.Image.Name)
	if imageName == "" {
		imageName = "image.png"
	}
	maskName := strings.TrimSpace(req.Mask.Name)
	if maskName == "" {
		maskName = "mask.png"
	}

	// Order is part of the contract with the guide prefix: mask first, base second.
	files := []InputFile{
		{Name: maskName, MimeType: "image/png", Data: req.Mask.Data},
		{Name: imageName, MimeType: req.Image.MimeType, Data: req.Image.Data},
	}
	opts := []GenerateOption{
		WithInputFiles(files),
		WithGeneratedImageDownload(true),
	}
	if req.Model != "" {
		opts = append(opts, WithModel(req.Model))
	}
	response, err := c.GenerateContent(ctx, buildMaskEditPrompt(instruction, req.Size), opts...)
	if err != nil {
		return nil, err
	}
	var images []Image
	for _, image := range response.Images {
		if image.Generated {
			images = append(images, image)
		}
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("provider returned no edited images")
	}
	return &ImageEditResult{Created: time.Now().Unix(), Data: images}, nil
}

// SetMaskedEditFunc replaces the masked-edit implementation; used by tests of
// the HTTP layer to capture requests without touching the network.
func (c *Client) SetMaskedEditFunc(fn func(ctx context.Context, req MaskedEditRequest) (*ImageEditResult, error)) {
	c.maskedEditFn = fn
}
